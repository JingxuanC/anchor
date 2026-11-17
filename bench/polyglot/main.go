// Command polyglot runs Aider-polyglot-style coding exercises against an agent
// and reports pass rates, anti-cheat flags, cost and token usage.
//
// The command is deliberately self-contained (Go standard library only) and
// runs offline with --mock-agent, which makes it suitable as a harness
// self-test in CI. A real run points --agent-cmd at an agent CLI; the prompt is
// appended as the final argument of that command:
//
//	go run ./bench/polyglot --polyglot-dir bench/fixtures/polyglot \
//	    --agent-cmd "claude -p" --trials 3 --out bench/results/polyglot-claude
//
// Exercises are loaded from an Aider-polyglot-compatible directory
// (<lang>/exercises/practice/<slug>/ or <lang>/<slug>/) and every trial starts
// from a fresh git repository in a temporary directory. Test files are
// restored from the pristine exercise before grading, so an agent that edits
// the tests is detected and does not get credit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	mockModeNormal   = "normal"
	mockModeSabotage = "sabotage"
	mockModeCheater  = "cheater"
)

const (
	defaultFixtures = "bench/fixtures/polyglot"
	defaultTimeout  = 10 * time.Minute
	maxDefaultTasks = 25
)

// Config is the resolved configuration of one benchmark run.
type Config struct {
	PolyglotDir string        // --polyglot-dir (defaults to --fixtures)
	Fixtures    string        // --fixtures, the bundled offline fixture dir
	Lang        string        // --lang filter (python, go, ...)
	Tasks       string        // --tasks all|<slug>[,<slug>...]
	Trials      int           // --trials, trials per (task, arm)
	Arm         string        // --arm baseline|anchor|both
	AgentCmd    string        // --agent-cmd
	AgentModel  string        // --agent-model, recorded metadata (optional)
	MockAgent   bool          // --mock-agent
	MockMode    string        // --mock-mode normal|sabotage|cheater
	Timeout     time.Duration // --timeout per agent/acceptance process
	MaxUSD      float64       // --max-usd, 0 = unlimited
	MaxTokens   int64         // --max-tokens, 0 = unlimited
	OutDir      string        // --out
	JSON        bool          // --json
	AnchorBin   string        // --anchor-bin
	KeepWork    bool          // --keep-work
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "polyglot:", err)
		os.Exit(2)
	}
	if cfg.JSON {
		// In JSON mode the summary would pollute stdout, so progress messages
		// stay on stderr and only the final report is printed to stdout.
	}
	report, err := Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "polyglot:", err)
		os.Exit(1)
	}
	if cfg.JSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "polyglot:", err)
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
	fs := flag.NewFlagSet("polyglot", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	polyglotDir := fs.String("polyglot-dir", "", "Aider-polyglot task directory (default: --fixtures)")
	fixtures := fs.String("fixtures", defaultFixtures, "bundled offline fixture directory")
	lang := fs.String("lang", "", "only run tasks in this language (e.g. python, go)")
	tasks := fs.String("tasks", "all", "tasks to run: all or a comma-separated list of slugs")
	trials := fs.Int("trials", 1, "trials per (task, arm)")
	arm := fs.String("arm", armBoth, "arm to run: baseline|anchor|both")
	agentCmd := fs.String("agent-cmd", "", "real agent CLI; the prompt is appended as the final argument")
	agentModel := fs.String("agent-model", "", "model identifier recorded in the report (optional)")
	mockAgent := fs.Bool("mock-agent", false, "use the deterministic offline mock agent")
	mockMode := fs.String("mock-mode", mockModeNormal, "mock agent behaviour: normal|sabotage|cheater")
	timeout := fs.Duration("timeout", defaultTimeout, "timeout per agent and acceptance process")
	maxUSD := fs.Float64("max-usd", 0, "stop launching trials once total cost exceeds this many USD (0 = unlimited)")
	maxTokens := fs.Int64("max-tokens", 0, "stop launching trials once total tokens exceed this count (0 = unlimited)")
	out := fs.String("out", "", "output directory (default bench/results/polyglot-<UTC timestamp>)")
	asJSON := fs.Bool("json", false, "print the report as JSON on stdout")
	anchorBin := fs.String("anchor-bin", "", "prebuilt anchor binary (default: build ./cmd/anchor into <out>/bin)")
	keepWork := fs.Bool("keep-work", false, "keep the per-trial project copies for debugging")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	cfg = Config{
		PolyglotDir: *polyglotDir,
		Fixtures:    *fixtures,
		Lang:        strings.ToLower(strings.TrimSpace(*lang)),
		Tasks:       *tasks,
		Trials:      *trials,
		Arm:         *arm,
		AgentCmd:    *agentCmd,
		AgentModel:  *agentModel,
		MockAgent:   *mockAgent,
		MockMode:    strings.ToLower(strings.TrimSpace(*mockMode)),
		Timeout:     *timeout,
		MaxUSD:      *maxUSD,
		MaxTokens:   *maxTokens,
		OutDir:      *out,
		JSON:        *asJSON,
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
	if cfg.MaxUSD < 0 {
		return cfg, fmt.Errorf("--max-usd must be zero or positive")
	}
	if cfg.MaxTokens < 0 {
		return cfg, fmt.Errorf("--max-tokens must be zero or positive")
	}
	switch cfg.MockMode {
	case mockModeNormal, mockModeSabotage, mockModeCheater:
	default:
		return cfg, fmt.Errorf("--mock-mode must be normal, sabotage or cheater (got %q)", cfg.MockMode)
	}
	switch {
	case cfg.MockAgent && cfg.AgentCmd != "":
		return cfg, fmt.Errorf("use either --mock-agent or --agent-cmd, not both")
	case !cfg.MockAgent && cfg.AgentCmd == "":
		return cfg, fmt.Errorf("--agent-cmd is required unless --mock-agent is set")
	}
	return cfg, nil
}

// benchmarkEnv is the immutable, run-wide context shared by every observation.
type benchmarkEnv struct {
	repoRoot    string
	polyglotDir string
	outDir      string
	anchor      anchorStatus
	acceptance  acceptanceEnv
	mockAgent   bool
}

// Run loads the tasks, executes every (task, trial, arm) observation and writes
// report.json/report.md. Setup failures for a single observation are recorded
// on that row instead of aborting the whole run.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	started := time.Now().UTC()
	repoRoot, _ := resolveRepoRoot()

	polyglotDir, err := resolvePolyglotDir(cfg, repoRoot)
	if err != nil {
		return nil, err
	}
	tasks, info, err := loadTasks(polyglotDir, cfg.Lang, cfg.Tasks)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no exercises found in %s", polyglotDir)
	}

	outDir, err := resolveOutDir(cfg, repoRoot, started)
	if err != nil {
		return nil, err
	}

	arms := armsFor(cfg.Arm)
	report := &Report{
		SchemaVersion: schemaVersion,
		GeneratedAt:   started.Format(time.RFC3339),
		Config: ReportConfig{
			PolyglotDir: polyglotDir,
			Lang:        cfg.Lang,
			Tasks:       cfg.Tasks,
			Trials:      cfg.Trials,
			Arm:         cfg.Arm,
			MockAgent:   cfg.MockAgent,
			MockMode:    mockModeForConfig(cfg),
			AgentCmd:    cfg.AgentCmd,
			AgentModel:  cfg.AgentModel,
			TimeoutMS:   cfg.Timeout.Milliseconds(),
			MaxUSD:      cfg.MaxUSD,
			MaxTokens:   cfg.MaxTokens,
			OutDir:      outDir,
		},
		Metadata: Metadata{
			GoVersion:      runtime.Version(),
			TaskWarnings:   info.Warnings,
			RepoRoot:       repoRoot,
			TaskCount:      len(tasks),
			TasksAvailable: info.Available,
			TasksCapped:    info.Capped,
		},
		Trials:      []TrialResult{},
		Aggregates:  map[string]ArmAggregate{},
		PerTask:     []TaskAggregate{},
		Environment: []string{},
		Artifacts: Artifacts{
			JSON:     filepath.Join(outDir, "report.json"),
			Markdown: filepath.Join(outDir, "report.md"),
			LogsDir:  filepath.Join(outDir, "logs"),
		},
	}
	if repoRoot != "" {
		report.Metadata.RepoCommit, report.Metadata.RepoDirty = gitRevision(ctx, repoRoot)
	}

	anchor := resolveAnchorStatus(ctx, cfg, repoRoot, outDir, includesAnchor(arms))
	report.Config.AnchorBin = anchor.Bin
	report.Metadata.AnchorVersion = anchor.Version
	report.Metadata.AnchorAvailable = anchor.Available
	report.Metadata.AnchorWrapSupported = anchor.WrapSupported
	report.Metadata.AnchorError = anchor.Error

	acceptance := detectAcceptanceEnv(ctx, tasks)
	report.Metadata.PythonAvailable = acceptance.PythonAvailable
	report.Metadata.PytestAvailable = acceptance.PytestAvailable
	report.Metadata.PythonVersion = acceptance.PythonVersion
	report.Metadata.PythonNote = acceptance.Note

	for _, task := range tasks {
		report.Metadata.Tasks = append(report.Metadata.Tasks, TaskMetadata{
			Slug:       task.Slug,
			Lang:       task.Lang,
			Hash:       task.Hash,
			Dir:        task.Dir,
			TestFiles:  append([]string{}, task.TestFiles...),
			Acceptance: append([]string{}, task.Acceptance...),
		})
	}

	env := &benchmarkEnv{
		repoRoot:    repoRoot,
		polyglotDir: polyglotDir,
		outDir:      outDir,
		anchor:      anchor,
		acceptance:  acceptance,
		mockAgent:   cfg.MockAgent,
	}

	if err := os.MkdirAll(filepath.Join(outDir, "logs"), 0o755); err != nil {
		return nil, err
	}

	if !cfg.MockAgent {
		if err := preflightAgent(cfg.AgentCmd); err != nil {
			report.Skipped = true
			report.SkipReason = err.Error()
			finalizeReport(report, tasks, arms, cfg, &Budget{MaxUSD: cfg.MaxUSD, MaxTokens: cfg.MaxTokens})
			if writeErr := writeReports(outDir, report); writeErr != nil {
				return report, writeErr
			}
			return report, nil
		}
	}

	budget := &Budget{MaxUSD: cfg.MaxUSD, MaxTokens: cfg.MaxTokens}
	total := len(tasks) * cfg.Trials * len(arms)
	done := 0
	truncated := false

outer:
	for trial := 1; trial <= cfg.Trials; trial++ {
		trialArms := arms
		if len(arms) == 2 && trial%2 == 0 {
			// Counterbalance the arm order across trials so build-cache and
			// machine warm-up effects do not systematically favour one arm.
			trialArms = []string{armAnchor, armBaseline}
		}
		for _, task := range tasks {
			for _, arm := range trialArms {
				if budget.Exceeded() {
					truncated = true
					budget.BudgetExceeded = true
					budget.Truncated = true
					break outer
				}
				done++
				fmt.Fprintf(os.Stderr, "polyglot: %d/%d task %s (~%s) trial %d/%d arm %s\n",
					done, total, task.Slug, task.Lang, trial, cfg.Trials, arm)
				row := runObservation(ctx, cfg, env, task, trial, arm)
				report.Trials = append(report.Trials, row)
				budget.Add(row.Tokens, row.CostUSD)
				if budget.Exceeded() {
					budget.BudgetExceeded = true
					budget.Reason = fmt.Sprintf("budget exceeded after task %s trial %d arm %s: cost=%.4f USD (max %.4f), tokens=%d (max %d)",
						row.Task, row.Trial, row.Arm, budget.TotalUSD, budget.MaxUSD, budget.TotalTokens, budget.MaxTokens)
					// Truncation means planned observations were skipped; a cap
					// that is reached by the very last observation is not a
					// truncation.
					if done < total {
						truncated = true
						budget.Truncated = true
					}
				}
				finalizeReport(report, tasks, arms, cfg, budget)
				if err := writeReports(outDir, report); err != nil {
					return report, err
				}
				if truncated {
					break outer
				}
			}
		}
	}
	if truncated && budget.Reason == "" {
		budget.Reason = "budget exceeded; remaining trials were not started"
	}
	finalizeReport(report, tasks, arms, cfg, budget)
	report.Environment = environmentNotes(cfg, report.Metadata, budget)
	if err := writeReports(outDir, report); err != nil {
		return report, err
	}
	return report, nil
}

func mockModeForConfig(cfg Config) string {
	if cfg.MockAgent {
		return cfg.MockMode
	}
	return ""
}

func resolvePolyglotDir(cfg Config, repoRoot string) (string, error) {
	dir := cfg.PolyglotDir
	if dir == "" {
		dir = cfg.Fixtures
	}
	if dir == "" {
		dir = defaultFixtures
	}
	resolved := resolvePath(dir, repoRoot)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("polyglot directory %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("polyglot directory %q is not a directory", resolved)
	}
	return resolved, nil
}

// resolvePath makes p absolute. Relative paths are resolved against the current
// directory first and against the repository root second, so the documented
// default works both from the repository root and from bench/polyglot.
func resolvePath(p, repoRoot string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if _, err := os.Stat(abs); err == nil {
		return abs
	}
	if repoRoot != "" {
		candidate := filepath.Join(repoRoot, p)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return abs
}

func resolveOutDir(cfg Config, repoRoot string, started time.Time) (string, error) {
	outDir := cfg.OutDir
	if outDir == "" {
		if repoRoot != "" {
			outDir = filepath.Join(repoRoot, "bench", "results", "polyglot-"+started.Format("20060102-150405"))
		} else {
			outDir = filepath.Join(os.TempDir(), "anchor-polyglot-"+started.Format("20060102-150405"))
		}
	}
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
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

// resolveRepoRoot walks up from the current directory looking for go.mod.
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

// gitRevision returns the repository HEAD commit and whether the working tree
// is dirty. Missing git or a non-repository directory yields empty values.
func gitRevision(ctx context.Context, dir string) (string, bool) {
	out, res := runCommand(ctx, 20*time.Second, dir, nil, "git", "rev-parse", "HEAD")
	if res.ExitCode != 0 {
		return "", false
	}
	commit := strings.TrimSpace(out)
	status, res := runCommand(ctx, 20*time.Second, dir, nil, "git", "status", "--porcelain")
	if res.ExitCode != 0 {
		return commit, false
	}
	return commit, strings.TrimSpace(status) != ""
}

func buildAnchor(ctx context.Context, repoRoot, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, res := runCommand(ctx, 3*time.Minute, repoRoot, map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off"}, goCommand(), "build", "-o", dst, "./cmd/anchor")
	if res.ExitCode != 0 {
		return fmt.Errorf("go build ./cmd/anchor: %s", firstLine(out))
	}
	return nil
}

func goCommand() string {
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

func preflightAgent(agentCmd string) error {
	argv, err := splitCommand(agentCmd)
	if err != nil {
		return err
	}
	bin := argv[0]
	if strings.ContainsRune(bin, os.PathSeparator) {
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

func environmentNotes(cfg Config, md Metadata, budget *Budget) []string {
	notes := []string{
		"This is an Aider-polyglot-style harness: each observation copies one exercise into a fresh temporary git repository and runs one agent process on it.",
		"There is no container or sandbox isolation: the agent runs with the invoking user's permissions, filesystem access and (unless the agent itself blocks it) network access. Do not run an untrusted agent this way.",
		"The prompt asks the agent not to modify acceptance tests; the harness additionally hashes the canonical test files before and after the agent, restores them before grading and records TestsModified/CheatFlags. This is an integrity check for benchmark purposes, not a security boundary.",
		"Exercise fixtures use fixed git author/committer dates so every trial starts from a reproducible commit.",
	}
	if cfg.MockAgent {
		notes = append(notes,
			"This run used --mock-agent: a deterministic, offline scripted agent. It validates the harness mechanics, anti-cheat handling and report shape; it is NOT empirical evidence about real agents.",
			"The mock agent prints a synthetic Claude-style JSON result line (usage + total_cost_usd) on every run so that usage/cost parsing and budget caps are exercised without network access.",
		)
	} else {
		notes = append(notes,
			"This run used a real agent command. The harness verified the binary exists but cannot verify authentication or tool-use behaviour; inspect the per-observation agent.log if trials fail for environmental reasons.",
			"Agent prompt mode: the prompt is appended as the final argv element, matching `claude -p \"...\"` and `codex exec \"...\"`.",
		)
	}
	if md.TaskCount > 0 && md.PythonAvailable && !md.PytestAvailable {
		notes = append(notes,
			"python3 was found but pytest is not installed, so Python acceptance runs are marked acceptance_skipped instead of being counted as passes.",
		)
	}
	if !md.PythonAvailable && hasPythonTasks(md) {
		notes = append(notes,
			"python3 was not found, so Python acceptance runs are marked acceptance_skipped instead of being counted as passes.",
		)
	}
	if md.TasksCapped {
		notes = append(notes,
			fmt.Sprintf("The default task selection was capped at %d exercises; use --tasks to select a specific subset.", maxDefaultTasks),
		)
	}
	if budget != nil && budget.Truncated {
		notes = append(notes,
			"The run was truncated by the configured budget cap; remaining (task, trial, arm) observations were not started.",
		)
	}
	if includesAnchor(armsFor(cfg.Arm)) {
		switch {
		case !md.AnchorAvailable:
			notes = append(notes, "The anchor binary was unavailable, so anchor-arm observations fell back to a direct agent run with ANCHOR_* environment variables.")
		case !md.AnchorWrapSupported:
			notes = append(notes, "The anchor binary has no `wrap` command; anchor-arm observations ran the agent directly with ANCHOR_* environment variables.")
		}
	}
	return notes
}

func hasPythonTasks(md Metadata) bool {
	for _, t := range md.Tasks {
		if t.Lang == "python" {
			return true
		}
	}
	return false
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
