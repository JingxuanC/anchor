// Command stress is the Anchor multi-session continuity stress benchmark.
//
// It runs small authored Go tasks in two phases: a phase-1 process receives a
// randomly chosen hidden convention that the visible tests do not pin down and
// must record it durably itself, then a fresh phase-2 process must implement a
// new function consistently with that convention. Acceptance is the phase-1
// visible tests plus a hidden test for the chosen variant.
//
// The default mode is offline and deterministic: --mock-agent scripts a fake
// agent process so the harness mechanics, acceptance discrimination, ablations
// and reporting can be self-tested. A real run uses --agent-cmd, e.g.
//
//	go run ./bench/stress --mock-agent --trials 2 --out /tmp/stress
//	go run ./bench/stress --trials 5 --agent-cmd "claude -p" --out bench/results/stress-claude
//
// The prompt text is appended as the final argument of the agent command unless
// the command contains {prompt} or {prompt_file}.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	// The mock agent is the same executable re-entered in a child process. This
	// keeps mock mode a genuine fresh-process test without compiling a second
	// helper binary.
	if os.Getenv(envMockExec) == "1" {
		os.Exit(runMockAgent())
	}
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "stress:", err)
		return 2
	}
	report, err := Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(stderr, "stress:", err)
		return 1
	}
	if cfg.JSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "stress:", err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fmt.Fprint(stdout, cliSummary(report))
	return 0
}

func parseFlags(args []string, stderr io.Writer) (*Config, error) {
	fs := flag.NewFlagSet("stress", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usageText)
		fs.PrintDefaults()
	}

	mockAgent := fs.Bool("mock-agent", false, "run the offline scripted mock agent (harness self-test)")
	agentCmd := fs.String("agent-cmd", "", "real agent command; the prompt is appended as the last argument")
	armsFlag := fs.String("arms", "baseline,rules,packet,full", "arms to run: baseline,rules,packet,full (full is also accepted as anchor)")
	tasksFlag := fs.String("tasks", "all", "tasks to run: all or a comma-separated fixture list (money,names,scores)")
	trials := fs.Int("trials", 3, "trials per (task, arm)")
	out := fs.String("out", "", "output directory (default: bench/results/stress-<timestamp>)")
	asJSON := fs.Bool("json", false, "print the report as JSON on stdout")
	agentModel := fs.String("agent-model", "", "agent model id recorded in the report")
	anchorBin := fs.String("anchor-bin", os.Getenv("ANCHOR_BIN"), "anchor binary to use (default: build from this checkout)")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout for each agent/acceptance command")
	maxUSD := fs.Float64("max-usd", 0, "stop after this reported cost in USD (0 = no cap)")
	maxTokens := fs.Int64("max-tokens", 0, "stop after this many reported tokens (0 = no cap)")
	keepWork := fs.Bool("keep-work", false, "keep per-trial project working trees (they are removed by default)")
	seed := fs.Int64("seed", 0, "variant-assignment seed (0 = time-based; same seed reproduces the assignment)")
	mockMode := fs.String("mock-mode", mockModeNormal, "mock agent mode(s): normal,sabotage,cheater (comma list cycles per trial)")
	fixtures := fs.String("fixtures", "", "fixtures root (default: bench/fixtures/stress of this checkout)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", strings.Join(fs.Args(), " "))
	}
	cfg := &Config{
		Trials:      *trials,
		MockAgent:   *mockAgent,
		AgentCmd:    *agentCmd,
		AgentModel:  *agentModel,
		AnchorBin:   *anchorBin,
		Timeout:     *timeout,
		OutDir:      *out,
		JSON:        *asJSON,
		KeepWork:    *keepWork,
		MaxUSD:      *maxUSD,
		MaxTokens:   *maxTokens,
		Seed:        *seed,
		FixturesDir: *fixtures,
		Stdout:      stderr,
	}
	if cfg.Trials < 1 {
		return nil, fmt.Errorf("--trials must be at least 1")
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("--timeout must be positive")
	}
	if cfg.MaxUSD < 0 || cfg.MaxTokens < 0 {
		return nil, fmt.Errorf("--max-usd/--max-tokens must be non-negative")
	}
	if cfg.MockAgent && strings.TrimSpace(cfg.AgentCmd) != "" {
		return nil, fmt.Errorf("--mock-agent and --agent-cmd are mutually exclusive")
	}
	if !cfg.MockAgent && strings.TrimSpace(cfg.AgentCmd) == "" {
		return nil, fmt.Errorf("one of --mock-agent or --agent-cmd is required")
	}
	arms, err := parseArms(*armsFlag)
	if err != nil {
		return nil, err
	}
	cfg.Arms = arms
	if strings.TrimSpace(*tasksFlag) == "" || *tasksFlag == "all" {
		cfg.Tasks = nil
	} else {
		for _, t := range strings.Split(*tasksFlag, ",") {
			if t = strings.TrimSpace(t); t != "" {
				cfg.Tasks = append(cfg.Tasks, t)
			}
		}
	}
	modes, err := parseMockModes(*mockMode)
	if err != nil {
		return nil, err
	}
	cfg.MockModes = modes
	return cfg, nil
}

func parseArms(s string) ([]string, error) {
	allowed := map[string]string{
		"baseline": armBaseline,
		"rules":    armRules,
		"packet":   armPacket,
		"full":     armFull,
		"anchor":   armFull,
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(s, ",") {
		name := strings.TrimSpace(strings.ToLower(raw))
		if name == "" {
			continue
		}
		arm, ok := allowed[name]
		if !ok {
			return nil, fmt.Errorf("unknown arm %q (want baseline,rules,packet,full)", raw)
		}
		if seen[arm] {
			continue
		}
		seen[arm] = true
		out = append(out, arm)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--arms selected no arms")
	}
	return out, nil
}

func parseMockModes(s string) ([]string, error) {
	allowed := map[string]bool{mockModeNormal: true, mockModeSabotage: true, mockModeCheater: true}
	var out []string
	for _, raw := range strings.Split(s, ",") {
		mode := strings.TrimSpace(strings.ToLower(raw))
		if mode == "" {
			continue
		}
		if !allowed[mode] {
			return nil, fmt.Errorf("unknown mock mode %q (want normal,sabotage,cheater)", raw)
		}
		out = append(out, mode)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--mock-mode selected no modes")
	}
	return out, nil
}

// defaultOutDir returns bench/results/stress-<timestamp> inside the module.
func defaultOutDir(moduleRoot string) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	if moduleRoot == "" {
		return ""
	}
	return filepath.Join(moduleRoot, "bench", "results", "stress-"+stamp)
}

const usageText = `stress — Anchor multi-session continuity stress benchmark

Runs three small Go tasks in two phases: a phase-1 process gets a randomly
chosen hidden convention and must record it durably itself, then a fresh
phase-2 process must implement a new function consistently with it. Acceptance
is the phase-1 visible tests plus the hidden test for the chosen variant.

Examples:
  go run ./bench/stress --mock-agent --trials 2 --out /tmp/stress
  go run ./bench/stress --mock-agent --arms baseline,full --trials 3 --json
  go run ./bench/stress --trials 5 --agent-cmd "claude -p" --out bench/results/stress-claude

Flags:
`
