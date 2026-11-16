// Command ab is the Anchor A/B benchmark harness.
//
// It measures an agent doing a small project task twice: once without Anchor
// (baseline arm) and once with Anchor session continuity (anchor arm). The
// second phase of each trial simulates context loss: a fresh agent process must
// resume the work, either from a short chat-style summary (baseline) or from
// Anchor's structured handoff packet and generated context file (anchor).
//
// The default mode is deterministic and offline: --mock-agent uses a scripted
// agent instead of a real CLI, write a report.json/report.md pair and exit.
// A real A/B run is produced with --agent-cmd, for example:
//
//	go run ./bench/ab --trials 5 --agent-cmd "claude -p" --out bench/results/claude
//
// The prompt is appended as the final argument of the agent command (pass
// --prompt-stdin to feed it on stdin instead).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const schemaVersion = 1

const (
	armBaseline = "baseline"
	armAnchor   = "anchor"
	armBoth     = "both"
)

const (
	goalNodeID   = "goal-ab"
	phase1TaskID = "task-phase1"
	phase2TaskID = "task-phase2"
)

// materialSlowdownRatio and materialSlowdownAbsMS define H2 ("Anchor does not
// slow phase 1 materially"). The hypothesis is supported when the anchor arm's
// mean phase-1 time exceeds the baseline by at most this much.
const (
	materialSlowdownRatio = 1.20
	materialSlowdownAbsMS = 2000.0
)

// Config is the resolved configuration of one benchmark run.
type Config struct {
	Trials      int
	Arm         string
	AgentCmd    string
	MockAgent   bool
	Timeout     time.Duration
	Fixtures    string
	OutDir      string
	JSON        bool
	PromptStdin bool
	AgentModel  string
	AnchorBin   string
	KeepWork    bool
}

// Report is the machine-readable benchmark result written to report.json.
type Report struct {
	SchemaVersion int                         `json:"schema_version"`
	GeneratedAt   string                      `json:"generated_at"`
	Config        ReportConfig                `json:"config"`
	Skipped       bool                        `json:"skipped"`
	SkipReason    string                      `json:"skip_reason,omitempty"`
	Trials        []TrialResult               `json:"trials"`
	Aggregates    map[string]ArmAggregate     `json:"aggregates"`
	Delta         *Delta                      `json:"delta,omitempty"`
	Hypotheses    map[string]HypothesisResult `json:"hypotheses"`
	Environment   []string                    `json:"environment_notes"`
	Artifacts     ReportArtifacts             `json:"artifacts"`
}

// ReportConfig records exactly how a run was produced.
type ReportConfig struct {
	Trials        int    `json:"trials"`
	Arm           string `json:"arm"`
	MockAgent     bool   `json:"mock_agent"`
	AgentCmd      string `json:"agent_cmd,omitempty"`
	AgentModel    string `json:"agent_model,omitempty"`
	PromptMode    string `json:"prompt_mode"`
	PromptStdin   bool   `json:"prompt_stdin"`
	TimeoutMS     int64  `json:"timeout_ms"`
	FixturesDir   string `json:"fixtures_dir"`
	OutDir        string `json:"out_dir"`
	AnchorBin     string `json:"anchor_bin,omitempty"`
	WrapSupported bool   `json:"wrap_supported"`
	FixtureCommit string `json:"fixture_commit,omitempty"`
	GoVersion     string `json:"go_version"`
}

// ReportArtifacts lists the files written by a run.
type ReportArtifacts struct {
	JSON     string `json:"json"`
	Markdown string `json:"markdown"`
	LogsDir  string `json:"logs_dir"`
}

// TrialResult is one (trial, arm) observation.
type TrialResult struct {
	Trial int    `json:"trial"`
	Arm   string `json:"arm"`

	Phase1Pass     bool  `json:"phase1_pass"`
	Phase2Pass     bool  `json:"phase2_pass"`
	Phase1AgentMS  int64 `json:"phase1_agent_ms"`
	Phase1AcceptMS int64 `json:"phase1_accept_ms"`
	Phase1TotalMS  int64 `json:"phase1_total_ms"`
	Phase2AgentMS  int64 `json:"phase2_agent_ms"`
	Phase2AcceptMS int64 `json:"phase2_accept_ms"`
	Phase2TotalMS  int64 `json:"phase2_total_ms"`
	AnchorSetupMS  int64 `json:"anchor_setup_ms"`

	Phase1Tokens TokenUsage `json:"phase1_tokens"`
	Phase2Tokens TokenUsage `json:"phase2_tokens"`

	Phase1AgentExit  int    `json:"phase1_agent_exit"`
	Phase1AcceptExit int    `json:"phase1_accept_exit"`
	Phase2AgentExit  int    `json:"phase2_agent_exit"`
	Phase2AcceptExit int    `json:"phase2_accept_exit"`
	Phase1AcceptNote string `json:"phase1_accept_note,omitempty"`
	Phase2AcceptNote string `json:"phase2_accept_note,omitempty"`
	AgentTimedOut    bool   `json:"agent_timed_out"`

	AnchorEvents   int `json:"anchor_events"`
	AnchorCaptures int `json:"anchor_captures"`

	HandoffPrepared       bool   `json:"handoff_prepared"`
	HandoffComplete       bool   `json:"handoff_complete"`
	HandoffID             string `json:"handoff_id,omitempty"`
	HandoffRecoveryStatus string `json:"handoff_recovery_status,omitempty"`
	HandoffDecisions      int    `json:"handoff_decisions"`
	HandoffNextSteps      int    `json:"handoff_next_steps"`

	ResumeScore    int  `json:"resume_score"`
	DecisionMarker bool `json:"decision_marker"`

	WrapUsedP1    bool     `json:"wrap_used_phase1"`
	WrapUsedP2    bool     `json:"wrap_used_phase2"`
	WrapFallback  string   `json:"wrap_fallback,omitempty"`
	FixtureCommit string   `json:"fixture_commit,omitempty"`
	SetupCommit   string   `json:"setup_commit,omitempty"`
	LogDir        string   `json:"log_dir,omitempty"`
	WorkDir       string   `json:"work_dir,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

// ArmAggregate is the per-arm summary written to report.json.
type ArmAggregate struct {
	Trials              int     `json:"trials"`
	Phase1PassRate      float64 `json:"phase1_pass_rate"`
	Phase2PassRate      float64 `json:"phase2_pass_rate"`
	MeanPhase1TotalMS   float64 `json:"mean_phase1_total_ms"`
	MedianPhase1TotalMS float64 `json:"median_phase1_total_ms"`
	MeanPhase2TotalMS   float64 `json:"mean_phase2_total_ms"`
	MedianPhase2TotalMS float64 `json:"median_phase2_total_ms"`
	MeanResumeScore     float64 `json:"mean_resume_score"`
	MeanAnchorSetupMS   float64 `json:"mean_anchor_setup_ms"`
	MeanAnchorEvents    float64 `json:"mean_anchor_events"`
	MeanAnchorCaptures  float64 `json:"mean_anchor_captures"`
	HandoffPreparedRate float64 `json:"handoff_prepared_rate"`
	HandoffCompleteRate float64 `json:"handoff_complete_rate"`

	MeanPhase1InputTokens  float64 `json:"mean_phase1_input_tokens"`
	MeanPhase1OutputTokens float64 `json:"mean_phase1_output_tokens"`
	MeanPhase1CacheTokens  float64 `json:"mean_phase1_cache_tokens"`
	MeanPhase1CostUSD      float64 `json:"mean_phase1_cost_usd"`
	MeanPhase2InputTokens  float64 `json:"mean_phase2_input_tokens"`
	MeanPhase2OutputTokens float64 `json:"mean_phase2_output_tokens"`
	MeanPhase2CacheTokens  float64 `json:"mean_phase2_cache_tokens"`
	MeanPhase2CostUSD      float64 `json:"mean_phase2_cost_usd"`
	MeanTotalTokens        float64 `json:"mean_total_tokens"`
	UsageParsedTrials      int     `json:"usage_parsed_trials"`
}

// Delta is anchor minus baseline (ratios are anchor/baseline).
type Delta struct {
	Phase1PassRate    float64 `json:"phase1_pass_rate_delta"`
	Phase2PassRate    float64 `json:"phase2_pass_rate_delta"`
	MeanResumeScore   float64 `json:"mean_resume_score_delta"`
	MeanPhase1TotalMS float64 `json:"mean_phase1_total_ms_delta"`
	MeanPhase2TotalMS float64 `json:"mean_phase2_total_ms_delta"`
	MeanTotalTokens   float64 `json:"mean_total_tokens_delta"`
	TotalTokensRatio  float64 `json:"total_tokens_ratio"`
	MeanCostUSD       float64 `json:"mean_cost_usd_delta"`
	Phase1TotalRatio  float64 `json:"phase1_total_ratio"`
	Phase2TotalRatio  float64 `json:"phase2_total_ratio"`
	AnchorSetupMeanMS float64 `json:"anchor_setup_mean_ms"`
}

// HypothesisResult is the observed status of one pre-registered hypothesis.
type HypothesisResult struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ab:", err)
		os.Exit(2)
	}
	report, err := Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ab:", err)
		os.Exit(1)
	}
	if cfg.JSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ab:", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
	} else {
		fmt.Print(renderCLISummary(report))
	}
	if report.Skipped {
		os.Exit(3)
	}
}

func parseFlags(args []string) (Config, error) {
	var cfg Config
	fs := flag.NewFlagSet("ab", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	trials := fs.Int("trials", 3, "number of trials per arm")
	arm := fs.String("arm", armBoth, "arm to run: both|baseline|anchor")
	agentCmd := fs.String("agent-cmd", "", "real agent CLI, e.g. \"claude -p\" or \"codex exec --skip-git-repo-check\"")
	agentModel := fs.String("agent-model", "", "recorded model identifier for the run (e.g. deepseek-v4-flash)")
	mockAgent := fs.Bool("mock-agent", false, "use the deterministic scripted mock agent (offline, for CI)")
	timeout := fs.Duration("timeout", 600*time.Second, "timeout per phase (agent and acceptance)")
	fixtures := fs.String("fixtures", "", "fixtures directory (default bench/fixtures)")
	out := fs.String("out", "", "output directory (default bench/results/<UTC timestamp>)")
	asJSON := fs.Bool("json", false, "print the report as JSON on stdout")
	promptStdin := fs.Bool("prompt-stdin", false, "pass the prompt on stdin instead of appending it as the final argument")
	anchorBin := fs.String("anchor-bin", "", "prebuilt anchor binary (default: build ./cmd/anchor into the output dir)")
	keepWork := fs.Bool("keep-work", false, "keep the per-trial project copies for debugging")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	cfg = Config{
		Trials:      *trials,
		Arm:         *arm,
		AgentCmd:    *agentCmd,
		AgentModel:  *agentModel,
		MockAgent:   *mockAgent,
		Timeout:     *timeout,
		Fixtures:    *fixtures,
		OutDir:      *out,
		JSON:        *asJSON,
		PromptStdin: *promptStdin,
		AnchorBin:   *anchorBin,
		KeepWork:    *keepWork,
	}
	if cfg.Trials < 1 {
		return cfg, fmt.Errorf("--trials must be at least 1")
	}
	switch cfg.Arm {
	case armBoth, armBaseline, armAnchor:
	default:
		return cfg, fmt.Errorf("--arm must be both, baseline or anchor (got %q)", cfg.Arm)
	}
	if cfg.Timeout <= 0 {
		return cfg, fmt.Errorf("--timeout must be positive")
	}
	switch {
	case cfg.MockAgent && cfg.AgentCmd != "":
		return cfg, fmt.Errorf("use either --mock-agent or --agent-cmd, not both")
	case !cfg.MockAgent && cfg.AgentCmd == "":
		return cfg, fmt.Errorf("--agent-cmd is required unless --mock-agent is set")
	}
	return cfg, nil
}

// Run executes the benchmark and writes report.json and report.md.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	started := time.Now().UTC()

	repoRoot, err := resolveRepoRoot()
	if err != nil {
		return nil, err
	}
	fixturesDir := cfg.Fixtures
	if fixturesDir == "" {
		fixturesDir = filepath.Join(repoRoot, "bench", "fixtures")
	}
	if fixturesDir, err = filepath.Abs(fixturesDir); err != nil {
		return nil, err
	}
	if err := validateFixtures(fixturesDir); err != nil {
		return nil, err
	}
	outDir := cfg.OutDir
	if outDir == "" {
		outDir = filepath.Join(repoRoot, "bench", "results", started.Format("20060102-150405"))
	}
	if outDir, err = filepath.Abs(outDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(outDir, "logs"), 0o755); err != nil {
		return nil, err
	}

	arms := armsFor(cfg.Arm)
	report := &Report{
		SchemaVersion: schemaVersion,
		GeneratedAt:   started.Format(time.RFC3339),
		Config: ReportConfig{
			Trials:      cfg.Trials,
			Arm:         cfg.Arm,
			MockAgent:   cfg.MockAgent,
			AgentCmd:    cfg.AgentCmd,
			AgentModel:  cfg.AgentModel,
			PromptMode:  promptMode(cfg),
			PromptStdin: cfg.PromptStdin,
			TimeoutMS:   cfg.Timeout.Milliseconds(),
			FixturesDir: fixturesDir,
			OutDir:      outDir,
			GoVersion:   runtime.Version(),
		},
		Trials:      []TrialResult{},
		Aggregates:  map[string]ArmAggregate{},
		Hypotheses:  map[string]HypothesisResult{},
		Environment: environmentNotes(cfg),
		Artifacts: ReportArtifacts{
			JSON:     filepath.Join(outDir, "report.json"),
			Markdown: filepath.Join(outDir, "report.md"),
			LogsDir:  filepath.Join(outDir, "logs"),
		},
	}

	anchorBin := cfg.AnchorBin
	if anchorBin == "" && includesAnchor(arms) {
		anchorBin = filepath.Join(outDir, "bin", "anchor")
		if err := buildAnchor(ctx, repoRoot, anchorBin); err != nil {
			return report, fmt.Errorf("build anchor binary: %w", err)
		}
	} else if anchorBin != "" {
		if anchorBin, err = filepath.Abs(anchorBin); err != nil {
			return report, err
		}
	}
	report.Config.AnchorBin = anchorBin
	wrapSupported := false
	if includesAnchor(arms) {
		wrapSupported = detectWrap(ctx, anchorBin)
	}
	report.Config.WrapSupported = wrapSupported

	if !cfg.MockAgent {
		if err := preflightAgent(cfg.AgentCmd); err != nil {
			report.Skipped = true
			report.SkipReason = err.Error()
			report.Aggregates = aggregateTrials(nil, arms)
			report.Hypotheses = evaluateHypotheses(report)
			if writeErr := writeReports(outDir, report); writeErr != nil {
				return report, writeErr
			}
			return report, nil
		}
	}

	resolved := cfg
	resolved.Fixtures = fixturesDir
	resolved.OutDir = outDir
	resolved.AnchorBin = anchorBin

	// Warm the shared Go build/test cache once so the first trial's acceptance
	// run does not pay the compilation cost that later runs avoid.
	warmUpFixtureBuild(ctx, resolved)

	for trial := 1; trial <= cfg.Trials; trial++ {
		trialArms := arms
		if len(arms) == 2 && trial%2 == 0 {
			// Counterbalance the arm order across trials so build-cache and
			// machine warm-up effects do not systematically favour one arm.
			trialArms = []string{armAnchor, armBaseline}
		}
		for _, arm := range trialArms {
			fmt.Fprintf(os.Stderr, "ab: trial %d/%d arm %s\n", trial, cfg.Trials, arm)
			result := runTrial(ctx, resolved, trial, arm, wrapSupported)
			if report.Config.FixtureCommit == "" {
				report.Config.FixtureCommit = result.FixtureCommit
			} else if result.FixtureCommit != "" && result.FixtureCommit != report.Config.FixtureCommit {
				result.Errors = append(result.Errors, "fixture commit differs from earlier trials; fixture copy is not reproducible")
			}
			report.Trials = append(report.Trials, result)
		}
		report.Aggregates = aggregateTrials(report.Trials, arms)
		report.Delta = computeDelta(report.Aggregates)
		report.Hypotheses = evaluateHypotheses(report)
		if err := writeReports(outDir, report); err != nil {
			return report, err
		}
	}

	report.Aggregates = aggregateTrials(report.Trials, arms)
	report.Delta = computeDelta(report.Aggregates)
	report.Hypotheses = evaluateHypotheses(report)
	if err := writeReports(outDir, report); err != nil {
		return report, err
	}
	return report, nil
}

func promptMode(cfg Config) string {
	if cfg.MockAgent {
		return "mock-script (prompt ignored by the mock agent, still recorded)"
	}
	if cfg.PromptStdin {
		return "stdin"
	}
	return "final-argument"
}

func armsFor(mode string) []string {
	switch mode {
	case armBaseline:
		return []string{armBaseline}
	case armAnchor:
		return []string{armAnchor}
	default:
		return []string{armBaseline, armAnchor}
	}
}

func includesAnchor(arms []string) bool {
	for _, a := range arms {
		if a == armAnchor {
			return true
		}
	}
	return false
}

// runTrial executes one (trial, arm) observation. Setup failures are recorded in
// the result instead of aborting the whole run so one bad trial does not lose
// the other measurements.
func runTrial(ctx context.Context, cfg Config, trial int, arm string, wrapSupported bool) TrialResult {
	res := TrialResult{Trial: trial, Arm: arm}
	logDir := filepath.Join(cfg.OutDir, "logs", fmt.Sprintf("trial-%02d-%s", trial, arm))
	res.LogDir = logDir
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		res.errf("create log dir: %v", err)
		return res
	}
	if cfg.MockAgent {
		if err := writeMockAgentScripts(cfg.Fixtures, logDir); err != nil {
			res.errf("write mock agent scripts: %v", err)
			return res
		}
	}

	workBase, err := os.MkdirTemp("", fmt.Sprintf("anchor-ab-t%02d-%s-", trial, arm))
	if err != nil {
		res.errf("create work dir: %v", err)
		return res
	}
	if cfg.KeepWork {
		res.WorkDir = workBase
	} else {
		defer func() { _ = os.RemoveAll(workBase) }()
	}
	proj := filepath.Join(workBase, "project")
	if err := copyTree(filepath.Join(cfg.Fixtures, "ratelimit"), proj); err != nil {
		res.errf("copy fixture project: %v", err)
		return res
	}
	if commit, err := gitInitAndCommit(ctx, proj, "initial fixture"); err != nil {
		res.errf("%v", err)
	} else {
		res.FixtureCommit = commit
	}

	var stateDir, fallbackContext, packetPath string
	var packet packetDoc
	hasPacket := false
	anchorCfg := anchorSession{AnchorBin: cfg.AnchorBin, UseWrap: wrapSupported}

	switch arm {
	case armBaseline:
		// No Anchor state: the baseline arm is the project copy plus a chat
		// transcript, nothing else.
	case armAnchor:
		setupStart := time.Now()
		initRes := runAnchorCLI(ctx, cfg.AnchorBin, proj, "init", "--attach", "--dir", proj, "--workspace", "ab-bench", "--device", "bench")
		if initRes.ExitCode != 0 {
			res.errf("anchor init --attach failed (exit %d): %s", initRes.ExitCode, firstLine(initRes.Stderr))
		}
		stateDir = filepath.Join(proj, ".anchor")
		anchorCfg.StateDir = stateDir
		res.Errors = append(res.Errors, seedTree(ctx, cfg.AnchorBin, stateDir)...)
		if err := writeAnchorRules(proj); err != nil {
			res.errf("write ANCHOR_RULES.md: %v", err)
		}
		if commit, err := gitCommitAll(ctx, proj, "anchor: attach workspace and seed session tree"); err != nil {
			res.errf("%v", err)
		} else {
			res.SetupCommit = commit
		}
		res.AnchorSetupMS = time.Since(setupStart).Milliseconds()
	default:
		res.errf("unknown arm %q", arm)
		return res
	}

	// ---------------------------------------------------------------- phase 1
	phase1Prompt := phase1PromptFor(cfg, arm)
	if err := writeFile(filepath.Join(logDir, "phase1-prompt.txt"), phase1Prompt); err != nil {
		res.errf("write phase1 prompt: %v", err)
	}
	anchorCfg.NodeID = phase1TaskID
	anchorCfg.DirectEnv = anchorEnv(cfg.AnchorBin, stateDir, phase1TaskID, fmt.Sprintf("bench-session-%02d-%s-p1", trial, arm), "")
	anchorCfg.WrapEnv = map[string]string{"ANCHOR_BIN": cfg.AnchorBin}
	phase1Session := (*anchorSession)(nil)
	if arm == armAnchor {
		phase1Session = &anchorCfg
	}
	p1 := runPhaseAgent(ctx, cfg, proj, logDir, 1, phase1Prompt, phase1Session)
	res.Phase1AgentMS = p1.Duration.Milliseconds()
	res.Phase1AgentExit = p1.ExitCode
	res.Phase1Tokens = p1.Usage
	res.AgentTimedOut = res.AgentTimedOut || p1.TimedOut
	res.WrapUsedP1 = p1.WrapUsed
	if p1.WrapFallback != "" {
		res.WrapFallback = p1.WrapFallback
	}
	if p1.ExitCode != 0 {
		res.errf("phase-1 agent exited %d%s", p1.ExitCode, timeoutSuffix(p1.TimedOut))
	}
	if commit, err := gitCommitAll(ctx, proj, "phase1: agent work"); err != nil {
		res.errf("%v", err)
	} else if res.FixtureCommit != "" && commit == "" {
		res.errf("phase-1 commit is empty")
	}
	acc1 := runAcceptance(ctx, cfg, proj, 1, filepath.Join(logDir, "acceptance-phase1.log"))
	res.Phase1AcceptMS = acc1.Duration.Milliseconds()
	res.Phase1AcceptExit = acc1.ExitCode
	res.Phase1Pass = acc1.Pass
	res.Phase1TotalMS = res.Phase1AgentMS + res.Phase1AcceptMS
	if !acc1.Pass {
		res.Phase1AcceptNote = fmt.Sprintf("acceptance failed (exit %d): %s", acc1.ExitCode, truncate(acc1.Output, 600))
	}
	if err := gitRestoreClean(ctx, proj); err != nil {
		res.errf("%v", err)
	}

	// ------------------------------------------------------- anchor handoff
	if arm == armAnchor {
		prep := runAnchorCLI(ctx, cfg.AnchorBin, proj, "handoff", "prepare", "--dir", stateDir, "--repo", proj)
		if prep.ExitCode != 0 {
			res.errf("handoff prepare failed (exit %d): %s", prep.ExitCode, firstLine(prep.Stderr))
		} else {
			var out handoffPrepareOut
			if err := json.Unmarshal([]byte(prep.Stdout), &out); err != nil {
				res.errf("parse handoff prepare output: %v", err)
			} else {
				res.HandoffID = out.HandoffID
				res.HandoffRecoveryStatus = out.RecoveryStatus
				packetPath = out.Path
				if packetPath == "" {
					res.errf("handoff prepare returned no packet path")
				} else if raw, err := os.ReadFile(packetPath); err != nil {
					res.errf("read handoff packet: %v", err)
				} else if err := json.Unmarshal(raw, &packet); err != nil {
					res.errf("parse handoff packet: %v", err)
				} else {
					hasPacket = true
				}
			}
		}
		res.HandoffPrepared = hasPacket
		if hasPacket {
			res.HandoffDecisions = len(packet.Decisions)
			res.HandoffNextSteps = len(packet.NextSteps)
			res.HandoffComplete = packet.Goal != nil && res.HandoffDecisions >= 3 && res.HandoffNextSteps >= 1
			if !res.HandoffComplete {
				res.errf("handoff is incomplete: goal=%v decisions=%d next_steps=%d",
					packet.Goal != nil, res.HandoffDecisions, res.HandoffNextSteps)
			}
		}
		fallbackContext = filepath.Join(logDir, "resume-context.md")
		contextText := renderFallbackContext(stateDir, packet, hasPacket)
		if err := writeFile(fallbackContext, contextText); err != nil {
			res.errf("write resume context: %v", err)
		}
	}

	// ---------------------------------------------------------------- phase 2
	var phase2Prompt string
	if arm == armAnchor {
		phase2Prompt = anchorPhase2Prompt(packetPath, packet, hasPacket)
	} else {
		phase2Prompt = baselinePhase2Prompt()
	}
	if err := writeFile(filepath.Join(logDir, "phase2-prompt.txt"), phase2Prompt); err != nil {
		res.errf("write phase2 prompt: %v", err)
	}
	anchorCfg.NodeID = phase2TaskID
	anchorCfg.DirectEnv = anchorEnv(cfg.AnchorBin, stateDir, phase2TaskID, fmt.Sprintf("bench-session-%02d-%s-p2", trial, arm), fallbackContext)
	anchorCfg.WrapEnv = map[string]string{"ANCHOR_BIN": cfg.AnchorBin}
	wrapped := (*anchorSession)(nil)
	if arm == armAnchor {
		wrapped = &anchorCfg
	}
	p2 := runPhaseAgent(ctx, cfg, proj, logDir, 2, phase2Prompt, wrapped)
	res.Phase2AgentMS = p2.Duration.Milliseconds()
	res.Phase2AgentExit = p2.ExitCode
	res.Phase2Tokens = p2.Usage
	res.AgentTimedOut = res.AgentTimedOut || p2.TimedOut
	res.WrapUsedP2 = p2.WrapUsed
	if p2.WrapFallback != "" && res.WrapFallback == "" {
		res.WrapFallback = p2.WrapFallback
	}
	if p2.ExitCode != 0 {
		res.errf("phase-2 agent exited %d%s", p2.ExitCode, timeoutSuffix(p2.TimedOut))
	}
	res.DecisionMarker = detectDecisionMarker(ctx, proj, p2.Output)
	acc2 := runAcceptance(ctx, cfg, proj, 2, filepath.Join(logDir, "acceptance-phase2.log"))
	res.Phase2AcceptMS = acc2.Duration.Milliseconds()
	res.Phase2AcceptExit = acc2.ExitCode
	res.Phase2Pass = acc2.Pass
	res.Phase2TotalMS = res.Phase2AgentMS + res.Phase2AcceptMS
	if !acc2.Pass {
		res.Phase2AcceptNote = fmt.Sprintf("acceptance failed (exit %d): %s", acc2.ExitCode, truncate(acc2.Output, 600))
	}
	res.ResumeScore = boolToInt(res.Phase2Pass) + boolToInt(res.DecisionMarker)

	if arm == armAnchor {
		res.AnchorEvents, res.AnchorCaptures = countAnchorState(stateDir)
	}
	return res
}

func (r *TrialResult) errf(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timeoutSuffix(timedOut bool) string {
	if timedOut {
		return " (timed out)"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Prompts

const phase2Task = `Phase 2 (resume). Add this method to the ratelimit package, keeping the
existing public API unchanged:

    func (l *Limiter) WaitTime(n int) time.Duration

WaitTime reports how long the caller must wait before n tokens can be acquired:
0 when they are available now, and -1 when n can never be satisfied. It must be
consistent with the conventions recorded during phase 1: clock injection,
whole-interval integer rounding, and the oversized-request / non-positive
request rules.

Grading: the harness runs go test -count=1 ./... in the project directory with
the canonical phase-1 tests plus a hidden phase-2 test. Do not edit test files.
When you are done, stop.`

const baselineResumeSummary = `Previous session summary (chat log only; no structured handoff survived):

  "Implemented the token-bucket limiter in ratelimit.go. The phase-1 acceptance
   tests pass. Next step: add WaitTime(n int) time.Duration for the resume
   phase. The exact rounding and oversized-request conventions were discussed
   in the previous session, but they were not written down anywhere durable."

Only the summary above and the current repository contents are available now.`

// anchorRulesText is written into the anchor arm's project as ANCHOR_RULES.md.
const anchorRulesText = `# Anchor session-continuity rules (benchmark treatment)

This project is managed by Anchor. The workspace state lives in the
.anchor/ directory (ignored by git). The Session Tree already contains the goal, the two phase
tasks and the phase-1 design decisions.

- The CLI is available as ` + "`$ANCHOR_BIN`" + ` and the state directory as
  ` + "`$ANCHOR_WORKSPACE`" + `; always pass ` + "`--dir \"$ANCHOR_WORKSPACE\"`" + `.
- Record your implementation as evidence, then close the phase-1 task:

      "$ANCHOR_BIN" evidence add --dir "$ANCHOR_WORKSPACE" --id ev-phase1-impl --node task-phase1 --kind implementation --file ratelimit.go
      "$ANCHOR_BIN" node transition --dir "$ANCHOR_WORKSPACE" --id task-phase1 --to in_progress
      "$ANCHOR_BIN" node transition --dir "$ANCHOR_WORKSPACE" --id task-phase1 --to done --evidence ev-phase1-impl

- The Session Tree is the durable memory across sessions. Do not rely on the chat
  transcript: a fresh agent will be resumed from ` + "`anchor handoff prepare`" + `.
`

func phase1PromptFor(cfg Config, arm string) string {
	task, err := os.ReadFile(filepath.Join(cfg.Fixtures, "ratelimit", "TASK.md"))
	taskText := string(task)
	if err != nil {
		taskText = "Implement the token-bucket rate limiter in ratelimit.go so that go test ./... passes. Keep the public API stable and do not edit test files.\n"
	}
	var b strings.Builder
	b.WriteString("You are an autonomous coding agent working in the current directory.\n\n")
	b.WriteString(taskText)
	b.WriteString("\n\nWork only inside the current directory. Run the acceptance tests yourself. When you are done, stop.\n")
	if arm == armAnchor {
		b.WriteString("\n## Anchor session-continuity tool (use it)\n\n")
		b.WriteString(anchorRulesText)
	}
	return b.String()
}

func baselinePhase2Prompt() string {
	return "You are a fresh agent resuming work on the Go project in the current directory.\n\n" +
		baselineResumeSummary + "\n\n" + phase2Task
}

func anchorPhase2Prompt(packetPath string, packet packetDoc, hasPacket bool) string {
	var b strings.Builder
	b.WriteString("You are a fresh agent resuming work on the Go project in the current directory with Anchor session continuity.\n\n")
	b.WriteString("A structured handoff was produced from the Session Tree. Anchor also exported these environment variables for this session:\n")
	b.WriteString("- ANCHOR_WORKSPACE: the workspace state directory\n")
	b.WriteString("- ANCHOR_NODE: task-phase2\n")
	b.WriteString("- ANCHOR_CONTEXT_FILE: a context file generated from the Session Tree\n\n")
	b.WriteString("Read the file named by the ANCHOR_CONTEXT_FILE environment variable before coding. It contains the goal, the recorded decisions and the open tasks.\n\n")
	if packetPath != "" {
		b.WriteString("The handoff packet itself is at: " + packetPath + "\n\n")
	}
	if hasPacket {
		b.WriteString(renderPacketSummary(packet))
		b.WriteString("\n")
		if packetPath != "" {
			b.WriteString("If Anchor reports a pending handoff, acknowledge it before recording new session state:\n")
			b.WriteString("    \"$ANCHOR_BIN\" handoff restore --dir \"$ANCHOR_WORKSPACE\" --repo . --packet \"" + packetPath + "\" --ack\n\n")
		}
	}
	b.WriteString(phase2Task)
	return b.String()
}

// ---------------------------------------------------------------------------
// Anchor CLI helpers

type anchorSession struct {
	AnchorBin string
	StateDir  string
	NodeID    string
	UseWrap   bool
	DirectEnv map[string]string
	WrapEnv   map[string]string
}

func anchorEnv(anchorBin, stateDir, nodeID, sessionID, contextFile string) map[string]string {
	env := map[string]string{
		"ANCHOR_BIN":       anchorBin,
		"ANCHOR_WORKSPACE": stateDir,
		"ANCHOR_NODE":      nodeID,
		"ANCHOR_SESSION":   sessionID,
	}
	if contextFile != "" {
		env["ANCHOR_CONTEXT_FILE"] = contextFile
	}
	return env
}

type cliResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

func runAnchorCLI(ctx context.Context, bin, dir string, args ...string) cliResult {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := cliResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Err = err
		}
	}
	return res
}

type seededDecision struct {
	ID          string
	Title       string
	Decision    string
	Rationale   string
	Alternative string
	Consequence string
}

func seededDecisions() []seededDecision {
	return []seededDecision{
		{
			ID:          "dec-clock",
			Title:       "Clock injection",
			Decision:    "The limiter and WaitTime read time only through the injected now func; never call time.Now when a clock is provided.",
			Rationale:   "Deterministic tests and reproducible behavior with a fake clock.",
			Alternative: "Call time.Now directly and accept non-deterministic tests.",
			Consequence: "WaitTime must use the same injected clock as the phase-1 API.",
		},
		{
			ID:          "dec-round",
			Title:       "Whole-interval integer rounding",
			Decision:    "Available tokens accrue with integer floor(elapsed/interval). WaitTime = deficit*interval - (elapsed mod interval): waits round up to the next whole refill boundary, using integer arithmetic only.",
			Rationale:   "Consistent with lazy refill and testable with a fake clock.",
			Alternative: "Return deficit*interval regardless of the partial interval (overstates waits).",
			Consequence: "Capacity 3, interval 10ms, drained at t=0, clock at t=25ms: WaitTime(3) == 5ms.",
		},
		{
			ID:          "dec-oversize",
			Title:       "Oversized requests are impossible",
			Decision:    "n > capacity can never be satisfied: Allow returns false and WaitTime returns -1; neither consumes nor reserves tokens.",
			Rationale:   "A request larger than the bucket can never complete and must not drain the bucket.",
			Alternative: "Return deficit*interval as if the bucket could grow.",
			Consequence: "WaitTime(4) == -1 for capacity 3 and Tokens() is unchanged.",
		},
		{
			ID:          "dec-nonpositive",
			Title:       "Non-positive requests are no-ops",
			Decision:    "n <= 0 is not a valid token request: Allow returns false and WaitTime returns 0.",
			Rationale:   "Matches the phase-1 Allow contract.",
			Alternative: "Treat n <= 0 as an error or as a free success.",
			Consequence: "WaitTime(0) == 0 and WaitTime(-5) == 0.",
		},
	}
}

// seedTree writes the goal, the two phase tasks and the design decisions into a
// fresh Anchor workspace. These are the decisions a well-behaved phase-1 agent
// records; the baseline arm has no equivalent durable record.
func seedTree(ctx context.Context, bin, stateDir string) []string {
	var errs []string
	add := func(args ...string) {
		res := runAnchorCLI(ctx, bin, stateDir, args...)
		if res.ExitCode != 0 {
			errs = append(errs, fmt.Sprintf("anchor %s failed (exit %d): %s",
				strings.Join(args[:minInt(2, len(args))], " "), res.ExitCode, firstLine(res.Stderr)))
		}
	}
	add("node", "create", "--dir", stateDir, "--id", goalNodeID, "--type", "goal",
		"--title", "A/B benchmark: token-bucket rate limiter",
		"--north-star", "Deliver a correct, tested token-bucket limiter with a stable public API and a verifiable resume handoff.",
		"--must", "keep the public API in ratelimit.go stable",
		"--must", "use only the Go standard library and stay offline",
		"--must-not", "edit acceptance test files",
		"--must-not", "hide a failing acceptance run",
		"--success", "phase-1 acceptance (go test ./...) passes",
		"--success", "resume WaitTime matches the recorded conventions",
		"--provenance", "agent_inferred")
	add("node", "create", "--dir", stateDir, "--id", phase1TaskID, "--type", "task",
		"--parent", goalNodeID,
		"--title", "Phase 1: implement token-bucket Allow/Tokens",
		"--provenance", "agent_inferred")
	add("node", "create", "--dir", stateDir, "--id", phase2TaskID, "--type", "task",
		"--parent", goalNodeID,
		"--title", "Phase 2 resume: implement WaitTime",
		"--provenance", "agent_inferred")
	for _, d := range seededDecisions() {
		add("node", "create", "--dir", stateDir, "--id", d.ID, "--type", "decision",
			"--parent", goalNodeID,
			"--title", d.Title,
			"--decision", d.Decision,
			"--rationale", d.Rationale,
			"--alternative", d.Alternative,
			"--consequence", d.Consequence,
			"--provenance", "agent_inferred")
	}
	return errs
}

func writeAnchorRules(proj string) error {
	return writeFile(filepath.Join(proj, "ANCHOR_RULES.md"), anchorRulesText)
}

// ---------------------------------------------------------------------------
// Agent execution

type agentRunResult struct {
	ExitCode     int
	TimedOut     bool
	Duration     time.Duration
	Output       string
	WrapUsed     bool
	WrapFallback string
	Usage        TokenUsage
}

// TokenUsage is the agent-reported token/cost usage for one phase. Claude Code
// reports it in the final `--output-format json` result object; other agents may
// report nothing, in which case Parsed stays false and the report says so.
type TokenUsage struct {
	Parsed      bool    `json:"parsed"`
	Input       int64   `json:"input_tokens"`
	Output      int64   `json:"output_tokens"`
	CacheCreate int64   `json:"cache_creation_input_tokens"`
	CacheRead   int64   `json:"cache_read_input_tokens"`
	CostUSD     float64 `json:"total_cost_usd"`
	Turns       int64   `json:"num_turns"`
}

func (u TokenUsage) Total() int64 { return u.Input + u.Output + u.CacheCreate + u.CacheRead }

// parseTokenUsage extracts usage from an agent log. It scans for the last JSON
// object that looks like a provider/agent result carrying `usage` or
// `total_cost_usd`; everything else in the log is ignored.
func parseTokenUsage(output string) TokenUsage {
	usage := TokenUsage{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if u, ok := obj["usage"].(map[string]any); ok {
			usage.Input = int64Field(u, "input_tokens")
			usage.Output = int64Field(u, "output_tokens")
			usage.CacheCreate = int64Field(u, "cache_creation_input_tokens")
			usage.CacheRead = int64Field(u, "cache_read_input_tokens")
			usage.Parsed = true
		}
		if v, ok := obj["total_cost_usd"].(float64); ok {
			usage.CostUSD = v
			usage.Parsed = true
		}
		if v, ok := obj["num_turns"].(float64); ok {
			usage.Turns = int64(v)
		}
	}
	return usage
}

func int64Field(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

// runPhaseAgent runs one agent process for a phase. For the anchor arm it tries
// `anchor wrap --dir <state> --node <node> -- <agent>` first (which exports the
// ANCHOR_* environment variables and records the session); if the binary has no
// wrap command it falls back to running the agent directly with the same
// environment variables, and records why.
func runPhaseAgent(ctx context.Context, cfg Config, proj, logDir string, phase int, prompt string, anchor *anchorSession) agentRunResult {
	scriptPath := filepath.Join(logDir, fmt.Sprintf("mock-agent-phase%d.sh", phase))
	childArgv := []string{"sh", scriptPath}
	if !cfg.MockAgent {
		var err error
		childArgv, err = splitCommand(cfg.AgentCmd)
		if err != nil {
			return agentRunResult{ExitCode: -1, Output: "invalid --agent-cmd: " + err.Error()}
		}
		if !cfg.PromptStdin {
			childArgv = append(append([]string{}, childArgv...), prompt)
		}
	}
	stdin := ""
	if !cfg.MockAgent && cfg.PromptStdin {
		stdin = prompt
	}

	if anchor != nil && anchor.UseWrap {
		wrapArgv := []string{anchor.AnchorBin, "wrap", "--dir", anchor.StateDir, "--node", anchor.NodeID, "--"}
		wrapArgv = append(wrapArgv, childArgv...)
		res := execAgent(ctx, cfg, proj, wrapArgv, anchor.WrapEnv, stdin,
			filepath.Join(logDir, fmt.Sprintf("phase%d-wrap-probe.log", phase)))
		if isMissingWrap(res.Output) {
			res.WrapFallback = "anchor wrap unavailable: " + firstLine(res.Output)
			res.WrapUsed = false
		} else {
			res.WrapUsed = true
			// Keep a single, canonical agent log per phase.
			_ = os.Rename(filepath.Join(logDir, fmt.Sprintf("phase%d-wrap-probe.log", phase)),
				filepath.Join(logDir, fmt.Sprintf("phase%d-agent.log", phase)))
			return res
		}
	}

	env := map[string]string{}
	if anchor != nil {
		for k, v := range anchor.DirectEnv {
			env[k] = v
		}
	}
	res := execAgent(ctx, cfg, proj, childArgv, env, stdin,
		filepath.Join(logDir, fmt.Sprintf("phase%d-agent.log", phase)))
	return res
}

func isMissingWrap(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{"unknown command", "flag provided but not defined", "usage: anchor wrap"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func execAgent(ctx context.Context, cfg Config, proj string, argv []string, extraEnv map[string]string, stdin, logPath string) agentRunResult {
	res := agentRunResult{ExitCode: -1}
	if len(argv) == 0 {
		res.Output = "empty agent command"
		return res
	}
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	logFile, err := os.Create(logPath)
	if err != nil {
		res.Output = "create agent log: " + err.Error()
		return res
	}
	defer func() { _ = logFile.Close() }()
	fmt.Fprintf(logFile, "# command: %s\n# cwd: %s\n# timeout: %s\n\n", strings.Join(argv, " "), proj, cfg.Timeout)

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	configureProcessGroup(cmd)
	// Kill the whole subtree on timeout: a surviving child would keep the
	// stdout pipe open and hang Wait.
	cmd.Cancel = func() error {
		killProcessTree(cmd)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = proj
	cmd.Env = mergeEnv(os.Environ(), extraEnv)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	w := io.MultiWriter(&out, logFile)
	cmd.Stdout = w
	cmd.Stderr = w

	started := time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(started)
	res.Output = out.String()
	res.Usage = parseTokenUsage(res.Output)
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
		res.ExitCode = -1
	case runErr == nil:
		res.ExitCode = 0
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Output += "\n" + runErr.Error()
		}
	}
	fmt.Fprintf(logFile, "\n# exit: %d timed_out=%v duration_ms=%d\n", res.ExitCode, res.TimedOut, res.Duration.Milliseconds())
	return res
}

// ---------------------------------------------------------------------------
// Acceptance

type acceptanceResult struct {
	Pass     bool
	ExitCode int
	Duration time.Duration
	Output   string
}

func runAcceptance(ctx context.Context, cfg Config, proj string, phase int, logPath string) acceptanceResult {
	if err := installAcceptance(cfg.Fixtures, proj, phase); err != nil {
		return acceptanceResult{ExitCode: -1, Output: "install acceptance tests: " + err.Error()}
	}
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	argv := []string{goCommand(), "test", "-count=1", "./..."}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	configureProcessGroup(cmd)
	// Kill the whole subtree on timeout: a surviving child would keep the
	// stdout pipe open and hang Wait.
	cmd.Cancel = func() error {
		killProcessTree(cmd)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = proj
	cmd.Env = mergeEnv(os.Environ(), map[string]string{
		"GOPROXY":     "off",
		"GOTOOLCHAIN": "local",
		"GO111MODULE": "on",
	})
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	started := time.Now()
	err := cmd.Run()
	res := acceptanceResult{Duration: time.Since(started), Output: out.String(), ExitCode: 0}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Output += "\n" + err.Error()
		}
	}
	res.Pass = res.ExitCode == 0
	if err := writeFile(logPath, fmt.Sprintf("# command: %s\n# exit: %d\n\n%s", strings.Join(argv, " "), res.ExitCode, res.Output)); err != nil {
		// Logging failure must not change the measurement.
		_ = err
	}
	return res
}

func installAcceptance(fixturesDir, proj string, phase int) error {
	phase1, err := os.ReadFile(filepath.Join(fixturesDir, "testdata", "acceptance", "phase1", "ratelimit_test.go"))
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(proj, "ratelimit_test.go"), string(phase1)); err != nil {
		return err
	}
	if phase == 2 {
		hidden, err := os.ReadFile(filepath.Join(fixturesDir, "testdata", "acceptance", "phase2", "wait_time_accept_test.go"))
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(proj, "wait_time_accept_test.go"), string(hidden)); err != nil {
			return err
		}
	}
	return nil
}

// warmUpFixtureBuild compiles the fixture's test binary once in a throwaway
// copy so per-trial acceptance timings are not dominated by first-run
// compilation. Failures are ignored: real trials will surface them.
func warmUpFixtureBuild(ctx context.Context, cfg Config) {
	base, err := os.MkdirTemp("", "anchor-ab-warmup-")
	if err != nil {
		return
	}
	defer func() { _ = os.RemoveAll(base) }()
	proj := filepath.Join(base, "project")
	if err := copyTree(filepath.Join(cfg.Fixtures, "ratelimit"), proj); err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, goCommand(), "test", "-count=1", "-run", "^$", "./...")
	cmd.Dir = proj
	cmd.Env = mergeEnv(os.Environ(), map[string]string{
		"GOPROXY":     "off",
		"GOTOOLCHAIN": "local",
		"GO111MODULE": "on",
	})
	_ = cmd.Run()
}

// ---------------------------------------------------------------------------
// Mock agent

func writeMockAgentScripts(fixturesDir, logDir string) error {
	phase1, err := os.ReadFile(filepath.Join(fixturesDir, "testdata", "mock", "phase1_ratelimit.go"))
	if err != nil {
		return err
	}
	wrong, err := os.ReadFile(filepath.Join(fixturesDir, "testdata", "mock", "phase2_wrong_ratelimit.go"))
	if err != nil {
		return err
	}
	correct, err := os.ReadFile(filepath.Join(fixturesDir, "testdata", "mock", "phase2_correct_ratelimit.go"))
	if err != nil {
		return err
	}
	for name, content := range map[string]string{
		"phase1":  string(phase1),
		"wrong":   string(wrong),
		"correct": string(correct),
	} {
		if strings.Contains(content, "\n"+mockHeredocTerminator+"\n") {
			return fmt.Errorf("mock source %s contains the heredoc terminator %q", name, mockHeredocTerminator)
		}
	}
	phase1Script := "#!/bin/sh\n# Deterministic mock agent (phase 1): writes the partial implementation and,\n# when Anchor environment variables are present, records evidence and closes\n# the phase-1 task through the Anchor CLI. No network, no randomness.\nset -eu\n" +
		"cat > ratelimit.go <<'" + mockHeredocTerminator + "'\n" + string(phase1) + mockHeredocTerminator + "\n" +
		"if [ -n \"${ANCHOR_WORKSPACE:-}\" ] && [ -n \"${ANCHOR_BIN:-}\" ]; then\n" +
		"  \"$ANCHOR_BIN\" evidence add --dir \"$ANCHOR_WORKSPACE\" --id ev-phase1-impl --node " + phase1TaskID + " --kind implementation --file ratelimit.go >/dev/null\n" +
		"  \"$ANCHOR_BIN\" node transition --dir \"$ANCHOR_WORKSPACE\" --id " + phase1TaskID + " --to in_progress >/dev/null\n" +
		"  \"$ANCHOR_BIN\" node transition --dir \"$ANCHOR_WORKSPACE\" --id " + phase1TaskID + " --to done --evidence ev-phase1-impl >/dev/null\n" +
		"fi\n" +
		"echo \"mock-agent phase1: wrote ratelimit.go\"\n"
	phase2Script := "#!/bin/sh\n# Deterministic mock agent (phase 2): if Anchor exported a context file that\n# contains the recorded decisions, apply them; otherwise fall back to a naive\n# guess (the simulated context-loss baseline).\nset -eu\n" +
		"context=\"${ANCHOR_CONTEXT_FILE:-}\"\n" +
		"if [ -n \"${ANCHOR_WORKSPACE:-}\" ] && [ -n \"$context\" ] && [ -r \"$context\" ] && grep -q 'dec-round' \"$context\" && grep -q 'dec-oversize' \"$context\"; then\n" +
		"  cat > ratelimit.go <<'" + mockHeredocTerminator + "'\n" + string(correct) + mockHeredocTerminator + "\n" +
		"  echo \"mock-agent phase2: applied dec-clock/dec-round/dec-oversize/dec-nonpositive from $context\"\n" +
		"else\n" +
		"  cat > ratelimit.go <<'" + mockHeredocTerminator + "'\n" + string(wrong) + mockHeredocTerminator + "\n" +
		"  echo \"mock-agent phase2: no Anchor context available; used a naive WaitTime guess\"\n" +
		"fi\n"
	if err := writeFile(filepath.Join(logDir, "mock-agent-phase1.sh"), phase1Script); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(logDir, "mock-agent-phase2.sh"), phase2Script); err != nil {
		return err
	}
	return nil
}

const mockHeredocTerminator = "ANCHOR_MOCK_EOF"

// ---------------------------------------------------------------------------
// Packet rendering

type handoffPrepareOut struct {
	HandoffID      string   `json:"handoff_id"`
	Path           string   `json:"path"`
	RecoveryStatus string   `json:"recovery_status"`
	Warnings       []string `json:"warnings"`
}

type packetDoc struct {
	HandoffID      string `json:"handoff_id"`
	RecoveryStatus string `json:"recovery_status"`
	Goal           *struct {
		ID              string   `json:"id"`
		Title           string   `json:"title"`
		NorthStar       string   `json:"north_star"`
		Must            []string `json:"must"`
		MustNot         []string `json:"must_not"`
		SuccessCriteria []string `json:"success_criteria"`
		Status          string   `json:"status"`
	} `json:"goal"`
	Decisions []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Decision  string `json:"decision"`
		Rationale string `json:"rationale"`
		Status    string `json:"status"`
	} `json:"decisions"`
	NextSteps []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	} `json:"next_steps"`
	CodeState struct {
		Head  string `json:"head"`
		Dirty bool   `json:"dirty"`
	} `json:"code_state"`
	Warnings []string `json:"warnings"`
}

func renderPacketSummary(p packetDoc) string {
	var b strings.Builder
	b.WriteString("Structured handoff summary (from the Anchor packet):\n")
	if p.Goal != nil {
		b.WriteString("\nGoal `" + p.Goal.ID + "`: " + p.Goal.Title + "\n")
		if p.Goal.NorthStar != "" {
			b.WriteString("- north star: " + p.Goal.NorthStar + "\n")
		}
		for _, m := range p.Goal.Must {
			b.WriteString("- must: " + m + "\n")
		}
		for _, m := range p.Goal.MustNot {
			b.WriteString("- must not: " + m + "\n")
		}
		for _, s := range p.Goal.SuccessCriteria {
			b.WriteString("- success: " + s + "\n")
		}
	}
	if len(p.Decisions) > 0 {
		b.WriteString("\nRecorded decisions:\n")
		for _, d := range p.Decisions {
			b.WriteString("- `" + d.ID + "` " + d.Title)
			if d.Decision != "" {
				b.WriteString(": " + d.Decision)
			}
			if d.Rationale != "" {
				b.WriteString(" (rationale: " + d.Rationale + ")")
			}
			b.WriteString("\n")
		}
	}
	if len(p.NextSteps) > 0 {
		b.WriteString("\nOpen next steps:\n")
		for _, n := range p.NextSteps {
			b.WriteString("- `" + n.ID + "` [" + n.Status + "] " + n.Title + "\n")
		}
	}
	if p.RecoveryStatus != "" || p.CodeState.Head != "" {
		fmt.Fprintf(&b, "\nRecovery status: %s; code head: %s; warnings: %d\n",
			orNone(p.RecoveryStatus), orNone(p.CodeState.Head), len(p.Warnings))
	}
	return b.String()
}

// renderFallbackContext is used when `anchor wrap` is unavailable: it mimics the
// context file wrap would generate so the phase-2 agent still receives the
// structured decisions.
func renderFallbackContext(stateDir string, p packetDoc, hasPacket bool) string {
	var b strings.Builder
	b.WriteString("# Anchor resume context\n\n")
	b.WriteString("- workspace: " + stateDir + "\n")
	b.WriteString("- node: " + phase2TaskID + "\n\n")
	if hasPacket {
		b.WriteString(renderPacketSummary(p))
		b.WriteString("\n")
	} else {
		b.WriteString("## Goal\n\n- " + goalNodeID + ": A/B benchmark: token-bucket rate limiter\n\n")
		b.WriteString("## Decisions\n\n")
		for _, d := range seededDecisions() {
			b.WriteString("- `" + d.ID + "` " + d.Title + ": " + d.Decision + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Read this file instead of relying on chat history; Anchor is the durable memory.\n")
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// ---------------------------------------------------------------------------
// Decision-use marker

var decisionMarkers = []string{
	"dec-round",
	"dec-oversize",
	"dec-clock",
	"dec-nonpositive",
	"whole refill boundary",
	"whole-interval",
}

// detectDecisionMarker is a heuristic for the resume-score bonus: it looks for a
// recorded decision id (or an explicit statement of one) in the phase-2 agent
// output or working-tree diff.
func detectDecisionMarker(ctx context.Context, proj, agentOutput string) bool {
	haystack := strings.ToLower(agentOutput)
	if diff, err := gitOutput(ctx, proj, "diff", "--", "."); err == nil {
		haystack += strings.ToLower(diff)
	}
	if status, err := gitOutput(ctx, proj, "status", "--porcelain"); err == nil {
		haystack += strings.ToLower(status)
	}
	for _, marker := range decisionMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// State/metric helpers

func countAnchorState(stateDir string) (events, captures int) {
	if stateDir == "" {
		return 0, 0
	}
	events = len(globFiles(filepath.Join(stateDir, "events"), "*.json"))
	captures = len(globFiles(filepath.Join(stateDir, "local", "captures"), "*.json"))
	return events, captures
}

func globFiles(dir, pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil
	}
	return matches
}

// ---------------------------------------------------------------------------
// Git helpers

func gitInitAndCommit(ctx context.Context, proj, message string) (string, error) {
	if out, err := gitOutputErr(ctx, proj, "init", "-q", "-b", "main"); err != nil {
		return "", fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, kv := range [][2]string{
		{"user.email", "bench@example.com"},
		{"user.name", "Anchor Benchmark"},
		{"commit.gpgsign", "false"},
	} {
		if out, err := gitOutputErr(ctx, proj, "config", kv[0], kv[1]); err != nil {
			return "", fmt.Errorf("git config %s: %w: %s", kv[0], err, strings.TrimSpace(string(out)))
		}
	}
	return gitCommitAll(ctx, proj, message)
}

func gitCommitAll(ctx context.Context, proj, message string) (string, error) {
	if out, err := gitOutputErr(ctx, proj, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := gitOutputErr(ctx, proj, "commit", "-q", "--allow-empty", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := gitOutput(ctx, proj, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRestoreClean(ctx context.Context, proj string) error {
	if out, err := gitOutputErr(ctx, proj, "checkout", "--", "."); err != nil {
		return fmt.Errorf("git checkout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := gitOutputErr(ctx, proj, "clean", "-fdq"); err != nil {
		return fmt.Errorf("git clean: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOutput(ctx context.Context, proj string, args ...string) (string, error) {
	out, err := gitOutputErr(ctx, proj, args...)
	return string(out), err
}

func gitOutputErr(ctx context.Context, proj string, args ...string) ([]byte, error) {
	full := append([]string{"-C", proj}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = mergeEnv(os.Environ(), fixedGitEnv())
	return cmd.CombinedOutput()
}

func fixedGitEnv() map[string]string {
	return map[string]string{
		"GIT_AUTHOR_DATE":    "2024-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE": "2024-01-01T00:00:00Z",
	}
}

// ---------------------------------------------------------------------------
// Environment helpers

func resolveRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find the repository root (go.mod) above %s", dir)
		}
		dir = parent
	}
}

func validateFixtures(fixtures string) error {
	required := []string{
		filepath.Join("ratelimit", "go.mod"),
		filepath.Join("ratelimit", "TASK.md"),
		filepath.Join("ratelimit", "ACCEPT.md"),
		filepath.Join("ratelimit", "ratelimit.go"),
		filepath.Join("ratelimit", "ratelimit_test.go"),
		filepath.Join("testdata", "acceptance", "phase1", "ratelimit_test.go"),
		filepath.Join("testdata", "acceptance", "phase2", "wait_time_accept_test.go"),
		filepath.Join("testdata", "mock", "phase1_ratelimit.go"),
		filepath.Join("testdata", "mock", "phase2_wrong_ratelimit.go"),
		filepath.Join("testdata", "mock", "phase2_correct_ratelimit.go"),
	}
	var missing []string
	for _, rel := range required {
		if info, err := os.Stat(filepath.Join(fixtures, rel)); err != nil || info.IsDir() {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("fixtures directory %s is missing: %s", fixtures, strings.Join(missing, ", "))
	}
	return nil
}

func buildAnchor(ctx context.Context, repoRoot, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, goCommand(), "build", "-o", dst, "./cmd/anchor")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build ./cmd/anchor: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func detectWrap(ctx context.Context, anchorBin string) bool {
	if anchorBin == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, anchorBin, "help").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "wrap")
}

func preflightAgent(agentCmd string) error {
	argv, err := splitCommand(agentCmd)
	if err != nil {
		return err
	}
	bin := argv[0]
	if strings.ContainsRune(bin, filepath.Separator) {
		if info, err := os.Stat(bin); err != nil || info.IsDir() {
			return fmt.Errorf("agent command %q is not an executable file", bin)
		}
		return nil
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("agent command %q was not found in PATH; install or log in to the agent CLI, or use --mock-agent for the offline harness check", bin)
	}
	return nil
}

func goCommand() string {
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// splitCommand splits an agent command line into argv, honouring single and
// double quotes but not shell expansion.
func splitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command %q", s)
	}
	if inWord {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}

// mergeEnv returns base with any ANCHOR_* variable and keys overridden in extra
// removed, then appends extra deterministically. This keeps trials from
// inheriting a developer's own Anchor session and makes the anchor environment
// explicit.
func mergeEnv(base []string, extra map[string]string) []string {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if strings.HasPrefix(k, "ANCHOR_") {
			continue
		}
		if _, ok := extra[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}

func environmentNotes(cfg Config) []string {
	notes := []string{
		"Hypotheses are pre-registered in bench/README.md; rates are descriptive over the configured number of trials and no confidence intervals are computed.",
		"The fixture is copied and committed with fixed git dates so every trial starts from the same commit.",
	}
	if cfg.MockAgent {
		notes = append(notes,
			"This run used --mock-agent: a deterministic, offline simulator. It validates the harness mechanics and the report shape; it is NOT empirical evidence about real agents.",
			"Mock validation context: Claude Code 2.1.252 is installed in this checkout's environment but `claude -p` prints \"Not logged in · Please run /login\"; codex-cli 0.141.0 with the local deepseek provider emitted no tool actions when the team tried `codex exec`. Real A/B numbers therefore must be produced by the user after logging in locally (see bench/README.md).",
		)
	} else {
		notes = append(notes,
			"This run used a real agent command. The harness verified the binary exists but cannot verify authentication or tool-action behavior; inspect the per-phase agent logs if trials fail for environmental reasons.",
			"Environment status of the development checkout where this harness was built: Claude Code 2.1.252 was not logged in and codex exec emitted no tool actions with the local deepseek provider, so the real A/B could not be produced there.",
		)
	}
	return notes
}

// ---------------------------------------------------------------------------
// Aggregation and hypotheses

func aggregateTrials(trials []TrialResult, arms []string) map[string]ArmAggregate {
	agg := map[string]ArmAggregate{}
	for _, arm := range arms {
		var rows []TrialResult
		for _, t := range trials {
			if t.Arm == arm {
				rows = append(rows, t)
			}
		}
		if len(rows) == 0 {
			continue
		}
		a := ArmAggregate{Trials: len(rows)}
		var p1Total, p2Total []float64
		var p1Pass, p2Pass, prepared, complete int
		var resume, events, captures, setup float64
		var p1in, p1out, p1cache, p1cost, p2in, p2out, p2cache, p2cost, total float64
		usageParsed := 0
		for _, row := range rows {
			if row.Phase1Pass {
				p1Pass++
			}
			if row.Phase2Pass {
				p2Pass++
			}
			if row.HandoffPrepared {
				prepared++
			}
			if row.HandoffComplete {
				complete++
			}
			p1Total = append(p1Total, float64(row.Phase1TotalMS))
			p2Total = append(p2Total, float64(row.Phase2TotalMS))
			resume += float64(row.ResumeScore)
			events += float64(row.AnchorEvents)
			captures += float64(row.AnchorCaptures)
			setup += float64(row.AnchorSetupMS)
			if row.Phase1Tokens.Parsed || row.Phase2Tokens.Parsed {
				usageParsed++
			}
			p1in += float64(row.Phase1Tokens.Input)
			p1out += float64(row.Phase1Tokens.Output)
			p1cache += float64(row.Phase1Tokens.CacheCreate + row.Phase1Tokens.CacheRead)
			p1cost += row.Phase1Tokens.CostUSD
			p2in += float64(row.Phase2Tokens.Input)
			p2out += float64(row.Phase2Tokens.Output)
			p2cache += float64(row.Phase2Tokens.CacheCreate + row.Phase2Tokens.CacheRead)
			p2cost += row.Phase2Tokens.CostUSD
			total += float64(row.Phase1Tokens.Total() + row.Phase2Tokens.Total())
		}
		n := float64(len(rows))
		a.Phase1PassRate = float64(p1Pass) / n
		a.Phase2PassRate = float64(p2Pass) / n
		a.MeanPhase1TotalMS = meanFloat(p1Total)
		a.MedianPhase1TotalMS = medianFloat(p1Total)
		a.MeanPhase2TotalMS = meanFloat(p2Total)
		a.MedianPhase2TotalMS = medianFloat(p2Total)
		a.MeanResumeScore = resume / n
		a.MeanAnchorSetupMS = setup / n
		a.MeanAnchorEvents = events / n
		a.MeanAnchorCaptures = captures / n
		a.HandoffPreparedRate = float64(prepared) / n
		a.HandoffCompleteRate = float64(complete) / n
		a.MeanPhase1InputTokens = p1in / n
		a.MeanPhase1OutputTokens = p1out / n
		a.MeanPhase1CacheTokens = p1cache / n
		a.MeanPhase1CostUSD = p1cost / n
		a.MeanPhase2InputTokens = p2in / n
		a.MeanPhase2OutputTokens = p2out / n
		a.MeanPhase2CacheTokens = p2cache / n
		a.MeanPhase2CostUSD = p2cost / n
		a.MeanTotalTokens = total / n
		a.UsageParsedTrials = usageParsed
		agg[arm] = a
	}
	return agg
}

func computeDelta(agg map[string]ArmAggregate) *Delta {
	b, okB := agg[armBaseline]
	a, okA := agg[armAnchor]
	if !okB || !okA {
		return nil
	}
	d := &Delta{
		Phase1PassRate:    a.Phase1PassRate - b.Phase1PassRate,
		Phase2PassRate:    a.Phase2PassRate - b.Phase2PassRate,
		MeanResumeScore:   a.MeanResumeScore - b.MeanResumeScore,
		MeanPhase1TotalMS: a.MeanPhase1TotalMS - b.MeanPhase1TotalMS,
		MeanPhase2TotalMS: a.MeanPhase2TotalMS - b.MeanPhase2TotalMS,
		MeanTotalTokens:   a.MeanTotalTokens - b.MeanTotalTokens,
		MeanCostUSD:       (a.MeanPhase1CostUSD + a.MeanPhase2CostUSD) - (b.MeanPhase1CostUSD + b.MeanPhase2CostUSD),
	}
	if b.MeanPhase1TotalMS > 0 {
		d.Phase1TotalRatio = a.MeanPhase1TotalMS / b.MeanPhase1TotalMS
	}
	if b.MeanPhase2TotalMS > 0 {
		d.Phase2TotalRatio = a.MeanPhase2TotalMS / b.MeanPhase2TotalMS
	}
	if b.MeanTotalTokens > 0 {
		d.TotalTokensRatio = a.MeanTotalTokens / b.MeanTotalTokens
	}
	d.AnchorSetupMeanMS = a.MeanAnchorSetupMS
	return d
}

func evaluateHypotheses(report *Report) map[string]HypothesisResult {
	h := map[string]HypothesisResult{}
	if report.Skipped {
		detail := "run skipped: " + report.SkipReason
		h["H1"] = HypothesisResult{Status: "insufficient_data", Detail: detail}
		h["H2"] = HypothesisResult{Status: "insufficient_data", Detail: detail}
		h["H3"] = HypothesisResult{Status: "insufficient_data", Detail: detail}
		return h
	}
	b, hasB := report.Aggregates[armBaseline]
	a, hasA := report.Aggregates[armAnchor]

	if !hasB || !hasA {
		reason := "needs both arms; rerun with --arm both"
		h["H1"] = HypothesisResult{Status: "insufficient_data", Detail: reason}
		h["H2"] = HypothesisResult{Status: "insufficient_data", Detail: reason}
	} else {
		if a.Phase2PassRate > b.Phase2PassRate {
			h["H1"] = HypothesisResult{Status: "supported", Detail: fmt.Sprintf(
				"phase-2 pass rate anchor %.2f vs baseline %.2f across %d trial(s) per arm",
				a.Phase2PassRate, b.Phase2PassRate, a.Trials)}
		} else {
			h["H1"] = HypothesisResult{Status: "not_supported", Detail: fmt.Sprintf(
				"phase-2 pass rate anchor %.2f is not above baseline %.2f across %d trial(s) per arm",
				a.Phase2PassRate, b.Phase2PassRate, a.Trials)}
		}
		threshold := materialSlowdownAbsMS
		if b.MeanPhase1TotalMS*materialSlowdownRatio > threshold {
			threshold = b.MeanPhase1TotalMS * materialSlowdownRatio
		}
		delta := a.MeanPhase1TotalMS - b.MeanPhase1TotalMS
		if delta <= threshold {
			h["H2"] = HypothesisResult{Status: "supported", Detail: fmt.Sprintf(
				"phase-1 mean anchor %.0fms vs baseline %.0fms (delta %+.0fms, material threshold %.0fms)",
				a.MeanPhase1TotalMS, b.MeanPhase1TotalMS, delta, threshold)}
		} else {
			h["H2"] = HypothesisResult{Status: "not_supported", Detail: fmt.Sprintf(
				"phase-1 mean anchor %.0fms vs baseline %.0fms (delta %+.0fms exceeds material threshold %.0fms)",
				a.MeanPhase1TotalMS, b.MeanPhase1TotalMS, delta, threshold)}
		}
	}
	if !hasA || a.Trials == 0 {
		h["H3"] = HypothesisResult{Status: "insufficient_data", Detail: "needs the anchor arm"}
	} else if a.HandoffPreparedRate == 1 && a.HandoffCompleteRate == 1 {
		h["H3"] = HypothesisResult{Status: "supported", Detail: fmt.Sprintf(
			"%d/%d anchor trials produced a complete handoff packet (goal, >=3 decisions, next step)",
			int(a.HandoffCompleteRate*float64(a.Trials)), a.Trials)}
	} else {
		h["H3"] = HypothesisResult{Status: "not_supported", Detail: fmt.Sprintf(
			"handoff prepared rate %.2f, complete rate %.2f across %d anchor trial(s)",
			a.HandoffPreparedRate, a.HandoffCompleteRate, a.Trials)}
	}
	return h
}

func meanFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

func medianFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sorted := append([]float64{}, v...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// ---------------------------------------------------------------------------
// Report writing

func writeReports(outDir string, report *Report) error {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "report.json"), string(b)+"\n"); err != nil {
		return err
	}
	return writeFile(filepath.Join(outDir, "report.md"), renderMarkdown(report))
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func renderCLISummary(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Anchor A/B benchmark: %d trial(s) per arm, arm=%s, mock=%v, wrap=%v\n",
		report.Config.Trials, report.Config.Arm, report.Config.MockAgent, report.Config.WrapSupported)
	if report.Skipped {
		fmt.Fprintf(&b, "SKIPPED: %s\n", report.SkipReason)
	}
	fmt.Fprintf(&b, "%-9s %6s %8s %8s %11s %11s %8s %9s %9s %8s\n",
		"arm", "trials", "p1-pass", "p2-pass", "p1-mean-ms", "p2-mean-ms", "events", "tokens", "cost-usd", "resume")
	for _, arm := range []string{armBaseline, armAnchor} {
		a, ok := report.Aggregates[arm]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%-9s %6d %8.2f %8.2f %11.0f %11.0f %8.1f %9.0f %9.4f %8.2f\n",
			arm, a.Trials, a.Phase1PassRate, a.Phase2PassRate,
			a.MeanPhase1TotalMS, a.MeanPhase2TotalMS, a.MeanAnchorEvents,
			a.MeanTotalTokens, a.MeanPhase1CostUSD+a.MeanPhase2CostUSD, a.MeanResumeScore)
	}
	if report.Delta != nil {
		fmt.Fprintf(&b, "delta (anchor-baseline): p1-pass %+.2f p2-pass %+.2f p1-mean %+.0fms p2-mean %+.0fms tokens %+.0f cost %+.4f resume %+.2f\n",
			report.Delta.Phase1PassRate, report.Delta.Phase2PassRate,
			report.Delta.MeanPhase1TotalMS, report.Delta.MeanPhase2TotalMS,
			report.Delta.MeanTotalTokens, report.Delta.MeanCostUSD, report.Delta.MeanResumeScore)
	}
	for _, name := range []string{"H1", "H2", "H3"} {
		if hyp, ok := report.Hypotheses[name]; ok {
			fmt.Fprintf(&b, "%s: %s — %s\n", name, hyp.Status, hyp.Detail)
		}
	}
	fmt.Fprintf(&b, "report.json: %s\nreport.md:   %s\n", report.Artifacts.JSON, report.Artifacts.Markdown)
	return b.String()
}

func renderMarkdown(report *Report) string {
	var b strings.Builder
	b.WriteString("# Anchor A/B benchmark report\n\n")
	fmt.Fprintf(&b, "- generated: %s\n", report.GeneratedAt)
	fmt.Fprintf(&b, "- trials per arm: %d\n", report.Config.Trials)
	fmt.Fprintf(&b, "- arm mode: %s\n", report.Config.Arm)
	fmt.Fprintf(&b, "- agent: %s\n", agentDescription(report.Config))
	fmt.Fprintf(&b, "- prompt mode: %s\n", report.Config.PromptMode)
	fmt.Fprintf(&b, "- timeout per phase: %d ms\n", report.Config.TimeoutMS)
	fmt.Fprintf(&b, "- anchor wrap available: %v\n", report.Config.WrapSupported)
	fmt.Fprintf(&b, "- fixture commit: %s\n", orNone(report.Config.FixtureCommit))
	fmt.Fprintf(&b, "- fixtures: %s\n", report.Config.FixturesDir)
	fmt.Fprintf(&b, "- anchor binary: %s\n", orNone(report.Config.AnchorBin))
	fmt.Fprintf(&b, "- go: %s\n\n", report.Config.GoVersion)

	if report.Skipped {
		fmt.Fprintf(&b, "> **SKIPPED:** %s\n\n", report.SkipReason)
	}

	b.WriteString("## Results\n\n")
	b.WriteString("| arm | trials | phase-1 pass | phase-2 pass | mean p1 ms | median p1 ms | mean p2 ms | median p2 ms | mean events | mean captures | mean tokens | mean cost USD | handoff prepared | handoff complete | mean resume score |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, arm := range []string{armBaseline, armAnchor} {
		a, ok := report.Aggregates[arm]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %.2f | %.2f | %.0f | %.0f | %.0f | %.0f | %.1f | %.1f | %.0f | %.4f | %.2f | %.2f | %.2f |\n",
			arm, a.Trials, a.Phase1PassRate, a.Phase2PassRate,
			a.MeanPhase1TotalMS, a.MedianPhase1TotalMS, a.MeanPhase2TotalMS, a.MedianPhase2TotalMS,
			a.MeanAnchorEvents, a.MeanAnchorCaptures, a.MeanTotalTokens, a.MeanPhase1CostUSD+a.MeanPhase2CostUSD,
			a.HandoffPreparedRate, a.HandoffCompleteRate, a.MeanResumeScore)
	}
	b.WriteString("\nPhase times are agent wall time plus acceptance wall time; Anchor workspace setup is reported separately below.\n")
	b.WriteString("Token/cost means come from the agent's own `--output-format json` result object; trials where the agent printed no usage JSON are averaged as zero and counted in `usage_parsed_trials`.\n\n")

	if report.Delta != nil {
		b.WriteString("## Deltas (anchor minus baseline)\n\n")
		fmt.Fprintf(&b, "- phase-1 pass rate: %+.2f\n", report.Delta.Phase1PassRate)
		fmt.Fprintf(&b, "- phase-2 pass rate: %+.2f\n", report.Delta.Phase2PassRate)
		fmt.Fprintf(&b, "- mean resume score: %+.2f\n", report.Delta.MeanResumeScore)
		fmt.Fprintf(&b, "- mean phase-1 time: %+.0f ms (ratio %.2f)\n", report.Delta.MeanPhase1TotalMS, report.Delta.Phase1TotalRatio)
		fmt.Fprintf(&b, "- mean phase-2 time: %+.0f ms (ratio %.2f)\n", report.Delta.MeanPhase2TotalMS, report.Delta.Phase2TotalRatio)
		fmt.Fprintf(&b, "- mean total tokens: %+.0f (ratio %.2f)\n", report.Delta.MeanTotalTokens, report.Delta.TotalTokensRatio)
		fmt.Fprintf(&b, "- mean cost (USD): %+.4f\n", report.Delta.MeanCostUSD)
		fmt.Fprintf(&b, "- mean anchor workspace setup: %.0f ms (anchor arm only, excluded from phase times)\n\n", report.Delta.AnchorSetupMeanMS)
	}

	b.WriteString("## Hypotheses\n\n")
	b.WriteString("| id | hypothesis | observed status | detail |\n|---|---|---|---|\n")
	hypotheses := []struct{ id, text string }{
		{"H1", "Anchor improves phase-2 resume success (higher phase-2 pass rate and resume score)."},
		{"H2", "Anchor does not slow phase 1 materially (mean phase-1 time within the material threshold)."},
		{"H3", "Anchor records a complete handoff (goal, decisions, next steps) in every anchor trial."},
	}
	for _, hyp := range hypotheses {
		res, ok := report.Hypotheses[hyp.id]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", hyp.id, hyp.text, res.Status, res.Detail)
	}
	b.WriteString("\nH2 material threshold: anchor mean phase-1 time may exceed baseline by at most max(2000 ms, 20%) of the baseline mean.\n")

	b.WriteString("\n## Per-trial rows\n\n")
	b.WriteString("| trial | arm | p1 pass | p2 pass | p1 agent exit | p2 agent exit | p1 ms | p2 ms | p1 tokens | p2 tokens | p1 cost | p2 cost | events | captures | handoff prepared | handoff complete | resume score | wrap p1/p2 | errors |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, row := range report.Trials {
		notes := append([]string{}, row.Errors...)
		if row.Phase1AcceptNote != "" {
			notes = append(notes, "phase1: "+row.Phase1AcceptNote)
		}
		if row.Phase2AcceptNote != "" {
			notes = append(notes, "phase2: "+row.Phase2AcceptNote)
		}
		fmt.Fprintf(&b, "| %d | %s | %v | %v | %d | %d | %d | %d | %d | %d | %.4f | %.4f | %d | %d | %v | %v | %d | %v/%v | %s |\n",
			row.Trial, row.Arm, row.Phase1Pass, row.Phase2Pass, row.Phase1AgentExit, row.Phase2AgentExit,
			row.Phase1TotalMS, row.Phase2TotalMS, row.Phase1Tokens.Total(), row.Phase2Tokens.Total(),
			row.Phase1Tokens.CostUSD, row.Phase2Tokens.CostUSD,
			row.AnchorEvents, row.AnchorCaptures,
			row.HandoffPrepared, row.HandoffComplete, row.ResumeScore, row.WrapUsedP1, row.WrapUsedP2,
			markdownCell(strings.Join(notes, "; ")))
	}

	b.WriteString("\n## Environment and honest caveats\n\n")
	for _, note := range report.Environment {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	b.WriteString("\n### Statistical caveats\n\n")
	b.WriteString("- Pass rates are descriptive proportions over the configured N; with small N they have very wide uncertainty. Use at least 5 trials per arm, ideally 10+.\n")
	b.WriteString("- Arm order is counterbalanced across trials (odd trials baseline first, even trials anchor first) and the Go build cache is warmed once before the trials; still run the harness several times to control for machine drift and model updates.\n")
	b.WriteString("- The mock agent is a deterministic simulation. Mock results validate the harness, not Anchor's effect on real agents.\n")
	b.WriteString("- The anchor arm receives seeded decisions that represent phase-1 decisions recorded in the Session Tree; the baseline arm gets only a lossy chat summary. The hidden phase-2 test encodes one intended convention set, so a baseline agent can pass by guessing.\n")
	b.WriteString("- \"Decision marker\" (the resume-score bonus) is a heuristic: it looks for recorded decision ids or an explicit statement of the convention in the phase-2 agent output or diff.\n")
	b.WriteString("- Acceptance tests are restored from the fixtures before grading, so agents cannot weaken them, but an agent can still change the public API in ways phase-1 tests notice (that is graded).\n")
	return b.String()
}

func agentDescription(cfg ReportConfig) string {
	if cfg.MockAgent {
		return "deterministic mock agent (--mock-agent; offline)"
	}
	return "real agent command: " + cfg.AgentCmd
}

func markdownCell(s string) string {
	if s == "" {
		return "-"
	}
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Misc helpers

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
