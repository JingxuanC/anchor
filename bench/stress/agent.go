package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// commandResult is the outcome of one external command.
type commandResult struct {
	Output   string
	ExitCode int
	TimedOut bool
	Duration time.Duration
	Err      error
}

// runCommand runs argv in dir with env, enforcing timeout. Output is captured
// and also streamed to logW when non-nil. It never returns an error for a
// non-zero exit: callers decide whether that is an expected failure.
func runCommand(ctx context.Context, timeout time.Duration, dir string, env []string, logW io.Writer, argv ...string) commandResult {
	res := commandResult{ExitCode: -1}
	if len(argv) == 0 {
		res.Err = fmt.Errorf("empty command")
		return res
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var out bytes.Buffer
	w := io.Writer(&out)
	if logW != nil {
		w = io.MultiWriter(&out, logW)
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessTree(cmd)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = w
	cmd.Stderr = w

	started := time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(started)
	res.Output = out.String()
	if runCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.Err = runCtx.Err()
		return res
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(runErr, &exitErr); ok {
			res.ExitCode = exitErr.ExitCode()
			return res
		}
		res.Err = runErr
		return res
	}
	res.ExitCode = 0
	return res
}

// asExitError is a tiny wrapper so the import stays local and the helper is
// shared with the mock agent code.
func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// splitCommand splits an agent command line into argv fields. It understands
// single quotes, double quotes and backslash escaping; it deliberately does not
// run a shell (so it works identically on every platform).
func splitCommand(s string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	inSingle, inDouble, escaped, started := false, false, false, false
	flush := func() {
		if started {
			fields = append(fields, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			started = true
			escaped = false
		case r == '\\' && !inSingle:
			escaped = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unterminated escape/quote in command %q", s)
	}
	flush()
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty agent command")
	}
	return fields, nil
}

// agentArgv builds the argv for one agent invocation.
func agentArgv(cfg *Config, prompt, promptPath string) ([]string, error) {
	if cfg.MockAgent {
		if cfg.MockExec == "" {
			return nil, fmt.Errorf("mock agent executable is not resolved")
		}
		return []string{cfg.MockExec}, nil
	}
	base, err := splitCommand(cfg.AgentCmd)
	if err != nil {
		return nil, err
	}
	replaced := false
	for i, arg := range base {
		if strings.Contains(arg, "{prompt_file}") {
			base[i] = strings.ReplaceAll(arg, "{prompt_file}", promptPath)
			replaced = true
		}
		if strings.Contains(arg, "{prompt}") {
			base[i] = strings.ReplaceAll(arg, "{prompt}", prompt)
			replaced = true
		}
	}
	if !replaced {
		base = append(base, prompt)
	}
	return base, nil
}

// baseEnv returns the process environment with the given overrides applied.
func baseEnv(overrides map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// phaseEnv builds the environment for an agent invocation.
type phaseEnvSpec struct {
	phase           int
	task            string
	arm             string
	variant         string
	project         string
	prompt          string
	promptFile      string
	planFile        string
	mode            string
	anchorBin       string
	anchorWorkspace string
	contextFiles    []string
	packet          string
	rulesFile       string
}

func (s phaseEnvSpec) env() []string {
	overrides := map[string]string{
		envPhase:      fmt.Sprintf("%d", s.phase),
		envTask:       s.task,
		envArm:        s.arm,
		envProject:    s.project,
		envPromptFile: s.promptFile,
	}
	if s.variant != "" {
		overrides[envVariant] = s.variant
	}
	if s.anchorBin != "" {
		overrides["ANCHOR_BIN"] = s.anchorBin
	}
	if s.anchorWorkspace != "" {
		overrides[envAnchorWorkspace] = s.anchorWorkspace
	}
	if s.packet != "" {
		overrides[envPacket] = s.packet
	}
	if s.rulesFile != "" {
		overrides[envRulesFile] = s.rulesFile
	}
	if len(s.contextFiles) > 0 {
		b, _ := json.Marshal(s.contextFiles)
		overrides[envContextFiles] = string(b)
	}
	if s.planFile != "" {
		overrides[envMockPlan] = s.planFile
		overrides[envMockMode] = s.mode
		overrides[envMockExec] = "1"
	}
	return baseEnv(overrides)
}

// ---------------------------------------------------------------------------
// Token usage parsing

// tokenUsage is the reported token/cost usage for one phase. Mock mode reports
// synthetic numbers so budget behaviour can be exercised offline; real agents
// report whatever their CLI emits.
type tokenUsage struct {
	Parsed      bool
	Input       int64
	Output      int64
	CacheCreate int64
	CacheRead   int64
	CostUSD     float64
	Model       string
}

func (u tokenUsage) total() int64 { return u.Input + u.Output + u.CacheCreate + u.CacheRead }

// parseTokenUsage scans command output for JSON objects that look like an agent
// result carrying usage or total_cost_usd; the last such object wins.
func parseTokenUsage(output string) tokenUsage {
	var usage tokenUsage
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
			usage.Input = intField(u, "input_tokens")
			usage.Output = intField(u, "output_tokens")
			usage.CacheCreate = intField(u, "cache_creation_input_tokens")
			usage.CacheRead = intField(u, "cache_read_input_tokens")
			usage.Parsed = true
		}
		if v, ok := obj["total_cost_usd"].(float64); ok {
			usage.CostUSD = v
			usage.Parsed = true
		}
		if v, ok := obj["model"].(string); ok && v != "" {
			usage.Model = v
		}
		if mu, ok := obj["modelUsage"].(map[string]any); ok && usage.Model == "" {
			var models []string
			for m := range mu {
				models = append(models, m)
			}
			sort.Strings(models)
			if len(models) > 0 {
				usage.Model = models[0]
			}
		}
	}
	return usage
}

func intField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Mock agent plan

// mockPlan is the data file the harness hands to the scripted agent. The mock
// agent decides for itself whether the phase-2 context conveys the variant; the
// harness does not tell it pass/fail.
type mockPlan struct {
	Phase         int    `json:"phase"`
	Task          string `json:"task"`
	Arm           string `json:"arm"`
	MockMode      string `json:"mock_mode"`
	Project       string `json:"project"`
	PromptFile    string `json:"prompt_file"`
	Variant       string `json:"variant,omitempty"`
	VariantPhrase string `json:"variant_phrase,omitempty"`
	PackageName   string `json:"package_name"`

	Phase1File    string   `json:"phase1_file"`
	Phase1Code    string   `json:"phase1_code"`
	Phase1CodeBad string   `json:"phase1_code_bad"`
	VisibleTests  []string `json:"visible_tests"`
	CommitMessage string   `json:"commit_message"`
	Commit        bool     `json:"commit"`

	Decisions       []mockDecision `json:"decisions,omitempty"`
	AnchorWorkspace string         `json:"anchor_workspace,omitempty"`
	AnchorBin       string         `json:"anchor_bin,omitempty"`

	Phase2File    string `json:"phase2_file"`
	Phase2Correct string `json:"phase2_correct"`
	Phase2Wrong   string `json:"phase2_wrong"`

	ContextFiles []string  `json:"context_files,omitempty"`
	Packet       string    `json:"packet,omitempty"`
	RulesFile    string    `json:"rules_file,omitempty"`
	Usage        mockUsage `json:"usage"`
}

// mockDecision is one decision node the mock records through the anchor CLI.
type mockDecision struct {
	ID        string `json:"id"`
	Parent    string `json:"parent"`
	Title     string `json:"title"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

// mockUsage is the synthetic usage the mock agent reports on stdout.
type mockUsage struct {
	Input   int64   `json:"input_tokens"`
	Output  int64   `json:"output_tokens"`
	CostUSD float64 `json:"total_cost_usd"`
}

// writeMockPlan writes one mock plan file into the trial directory.
func writeMockPlan(path string, plan *mockPlan) error {
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
