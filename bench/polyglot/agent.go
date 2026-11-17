package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Token usage and agent output parsing

// TokenUsage is the agent-reported token/cost usage for one observation. Claude
// Code reports it in the final `--output-format json` result object; other
// agents may report nothing, in which case Parsed stays false and the report
// says so.
type TokenUsage struct {
	Parsed      bool    `json:"parsed"`
	Input       int64   `json:"input_tokens"`
	Output      int64   `json:"output_tokens"`
	CacheCreate int64   `json:"cache_creation_input_tokens"`
	CacheRead   int64   `json:"cache_read_input_tokens"`
	CostUSD     float64 `json:"total_cost_usd"`
	Turns       int64   `json:"num_turns"`
	Model       string  `json:"model,omitempty"`
}

// Total is the number of input+output+cache tokens reported by the agent.
func (u TokenUsage) Total() int64 { return u.Input + u.Output + u.CacheCreate + u.CacheRead }

// parseTokenUsage extracts usage from an agent log. It scans for JSON objects
// that look like a provider/agent result carrying `usage` or `total_cost_usd`
// and lets the last result win (some CLIs emit progress objects before the
// final result).
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
		if v, ok := obj["model"].(string); ok && v != "" {
			usage.Model = v
		}
		if mu, ok := obj["modelUsage"].(map[string]any); ok && usage.Model == "" {
			var models []string
			for model := range mu {
				models = append(models, model)
			}
			sort.Strings(models)
			if len(models) > 0 {
				usage.Model = models[0]
			}
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
	case int:
		return int64(v)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Process execution

type commandResult struct {
	Output   string
	ExitCode int
	TimedOut bool
	Duration time.Duration
	Err      error
}

// runManaged runs one command with a timeout, killing its whole process group on
// expiry. Output is captured in memory and, when logW is non-nil, streamed to it.
func runManaged(ctx context.Context, timeout time.Duration, dir string, env []string, logW io.Writer, argv []string) commandResult {
	res := commandResult{ExitCode: -1}
	if len(argv) == 0 {
		res.Err = errors.New("empty command")
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
	// Kill the whole subtree on timeout: a surviving child would keep the
	// output pipe open and hang Wait.
	cmd.Cancel = func() error {
		killProcessTree(cmd)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = w
	cmd.Stderr = w

	started := time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(started)
	res.Output = out.String()
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
		res.ExitCode = -1
		res.Err = context.DeadlineExceeded
	case runErr == nil:
		res.ExitCode = 0
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Err = runErr
			res.Output += "\n" + runErr.Error()
		}
	}
	return res
}

// runCommand is a convenience wrapper for short helper commands (git, anchor
// CLI, version probes). extraEnv overrides inherited values by key.
func runCommand(ctx context.Context, timeout time.Duration, dir string, extraEnv map[string]string, name string, args ...string) (string, commandResult) {
	env := mergeEnv(os.Environ(), extraEnv)
	res := runManaged(ctx, timeout, dir, env, nil, append([]string{name}, args...))
	return res.Output, res
}

// ---------------------------------------------------------------------------
// Agent execution

type agentRunResult struct {
	ExitCode     int
	TimedOut     bool
	Duration     time.Duration
	Output       string
	Usage        TokenUsage
	WrapUsed     bool
	WrapFallback string
}

// execAgent runs one agent process in proj, writing the combined stdout/stderr
// to logPath. The prompt is either appended by the caller (real agents) or
// ignored (mock script).
func execAgent(ctx context.Context, cfg Config, proj, logPath string, argv []string, extraEnv map[string]string, stdin string) agentRunResult {
	res := agentRunResult{ExitCode: -1}
	if len(argv) == 0 {
		res.Output = "empty agent command"
		return res
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		res.Output = "create agent log: " + err.Error()
		return res
	}
	defer func() { _ = logFile.Close() }()
	fmt.Fprintf(logFile, "# command: %s\n# cwd: %s\n# timeout: %s\n\n", strings.Join(argv, " "), proj, cfg.Timeout)

	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var out bytes.Buffer
	w := io.MultiWriter(&out, logFile)

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	configureProcessGroup(cmd)
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

// mergeEnv returns base with all inherited ANCHOR_* variables and any keys
// overridden by extra removed, then appends extra deterministically. This keeps
// trials from inheriting a developer's own Anchor session and makes the anchor
// environment explicit.
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

// ---------------------------------------------------------------------------
// Anchor CLI integration

type anchorStatus struct {
	Bin           string
	Available     bool
	WrapSupported bool
	Version       string
	Error         string
}

// resolveAnchorStatus locates or builds the anchor binary, records its version
// and whether the `wrap` command exists. A failure is recorded, never fatal:
// anchor-arm observations then fall back to a direct agent run with ANCHOR_*
// environment variables.
func resolveAnchorStatus(ctx context.Context, cfg Config, repoRoot, outDir string, needed bool) anchorStatus {
	var st anchorStatus
	if !needed {
		return st
	}
	if cfg.AnchorBin == "" {
		if repoRoot == "" {
			st.Error = "anchor binary was not provided and the repository root could not be found to build ./cmd/anchor"
			return st
		}
		st.Bin = filepath.Join(outDir, "bin", "anchor")
		if err := buildAnchor(ctx, repoRoot, st.Bin); err != nil {
			st.Error = "build anchor binary: " + err.Error()
			return st
		}
	} else {
		abs, err := filepath.Abs(cfg.AnchorBin)
		if err != nil {
			st.Bin = cfg.AnchorBin
			st.Error = err.Error()
			return st
		}
		st.Bin = abs
	}

	versionOut, versionRes := runCommand(ctx, 30*time.Second, repoRoot, nil, st.Bin, "version")
	helpOut, helpRes := runCommand(ctx, 30*time.Second, repoRoot, nil, st.Bin, "help")
	st.Available = versionRes.ExitCode == 0 || helpRes.ExitCode == 0
	if versionRes.ExitCode == 0 {
		st.Version = firstLine(versionOut)
	}
	if helpRes.ExitCode == 0 {
		st.WrapSupported = strings.Contains(helpOut, "wrap")
	}
	if !st.Available {
		detail := firstLine(helpOut)
		if detail == "" {
			detail = firstLine(versionOut)
		}
		st.Error = fmt.Sprintf("anchor binary %s is not usable (help exit %d): %s", st.Bin, helpRes.ExitCode, detail)
	}
	return st
}

type anchorWorkspace struct {
	StateDir string
	NodeID   string
	GoalID   string
}

// setupAnchorWorkspace initialises <proj>/.anchor and creates the goal + task
// nodes the wrapped session is attached to. It returns warnings rather than
// errors: a broken anchor setup degrades the anchor arm instead of losing the
// measurement.
func setupAnchorWorkspace(ctx context.Context, anchor anchorStatus, proj, taskSlug, logPath string) (anchorWorkspace, []string) {
	ws := anchorWorkspace{GoalID: "goal-polyglot", NodeID: "task-" + sanitizeNodeID(taskSlug)}
	var warnings []string
	if !anchor.Available {
		warnings = append(warnings, "anchor binary unavailable: "+firstLine(anchor.Error))
		return ws, warnings
	}
	var b strings.Builder
	defer func() { _ = writeFile(logPath, b.String()) }()
	record := func(label string, res commandResult) {
		fmt.Fprintf(&b, "$ anchor %s\n# exit: %d duration_ms: %d\n%s\n\n", label, res.ExitCode, res.Duration.Milliseconds(), res.Output)
	}
	logCmd := func(args ...string) commandResult {
		return runAnchorCLI(ctx, anchor.Bin, proj, args...)
	}

	initRes := logCmd("init", "--attach", "--dir", proj, "--agents", "none")
	record("init --attach --agents none", initRes)
	if initRes.ExitCode != 0 {
		warnings = append(warnings, "anchor init --attach failed: "+firstLine(initRes.Output))
		return ws, warnings
	}
	ws.StateDir = filepath.Join(proj, ".anchor")
	if info, err := os.Stat(ws.StateDir); err != nil || !info.IsDir() {
		warnings = append(warnings, "anchor init did not create "+ws.StateDir)
		return ws, warnings
	}
	goalRes := logCmd("node", "create", "--dir", ws.StateDir, "--id", ws.GoalID, "--type", "goal",
		"--title", "Aider-polyglot benchmark exercise",
		"--north-star", "Implement the exercise so its visible acceptance tests pass without changing those tests.",
		"--must-not", "modify or delete acceptance test files",
		"--success", "acceptance command passes with canonical tests restored",
		"--provenance", "agent_inferred")
	record("node create goal", goalRes)
	if goalRes.ExitCode != 0 {
		warnings = append(warnings, "anchor node create (goal) failed: "+firstLine(goalRes.Output))
	}
	taskRes := logCmd("node", "create", "--dir", ws.StateDir, "--id", ws.NodeID, "--type", "task",
		"--parent", ws.GoalID,
		"--title", "Exercise: "+taskSlug,
		"--provenance", "agent_inferred")
	record("node create task", taskRes)
	if taskRes.ExitCode != 0 {
		warnings = append(warnings, "anchor node create (task) failed: "+firstLine(taskRes.Output))
	}
	return ws, warnings
}

func runAnchorCLI(ctx context.Context, bin, dir string, args ...string) commandResult {
	if bin == "" || !filepath.IsAbs(bin) {
		// Nothing usable to run; report it the same way an exec failure would.
		return commandResult{ExitCode: -1, Err: fmt.Errorf("anchor binary %q is not available", bin)}
	}
	return runManaged(ctx, 2*time.Minute, dir, mergeEnv(os.Environ(), map[string]string{"ANCHOR_BIN": bin}), nil, append([]string{bin}, args...))
}

// ensureAnchorContext returns a context file for the fallback (direct) anchor
// arm. It asks the anchor CLI to render the session tree; if that fails it
// writes a minimal context file so ANCHOR_CONTEXT_FILE is always meaningful.
func ensureAnchorContext(ctx context.Context, anchor anchorStatus, stateDir, logDir, taskSlug string) string {
	if stateDir == "" {
		return ""
	}
	path := filepath.Join(logDir, "anchor-context.md")
	out, res := runCommand(ctx, time.Minute, stateDir, nil, anchor.Bin, "tree", "--dir", stateDir, "--md", path)
	if res.ExitCode == 0 {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path
		}
	}
	fallback := fmt.Sprintf("# Anchor workspace context\n\n- workspace: %s\n- task: %s\n- command failed: anchor tree exit %d\n%s\n",
		stateDir, taskSlug, res.ExitCode, truncate(out, 2000))
	if err := writeFile(path, fallback); err != nil {
		return ""
	}
	return path
}

func sanitizeNodeID(slug string) string {
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "task"
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Mock agent

// writeMockAgentScript writes the deterministic offline agent used by
// --mock-agent. Modes:
//
//	normal   copy <exercise>/mock/correct/* into the project
//	sabotage do nothing
//	cheater  copy <exercise>/mock/cheat/* over the visible test files
//
// Every mode prints a synthetic Claude-style JSON result line so token/cost
// parsing and budget caps are exercised offline.
func writeMockAgentScript(logDir string, task Task, mockMode string) (string, error) {
	path := filepath.Join(logDir, "mock-agent.sh")
	script := fmt.Sprintf(`#!/bin/sh
# Generated by bench/polyglot --mock-agent. The prompt is deliberately ignored.
set -u
mode="${POLY_MOCK_MODE:-%s}"
fixtures="${POLY_MOCK_FIXTURES:-}"
slug="${POLY_SLUG:-}"
printf 'mock-agent: mode=%%s slug=%%s cwd=%%s\n' "$mode" "$slug" "$PWD"
case "$mode" in
  normal)
    if [ -n "$fixtures" ] && [ -d "$fixtures/mock/correct" ]; then
      cp -R "$fixtures/mock/correct"/. .
      printf 'mock-agent: applied files from mock/correct\n'
    else
      printf 'mock-agent: no mock/correct directory; nothing applied\n'
    fi
    ;;
  sabotage)
    printf 'mock-agent: sabotage mode is a deliberate no-op\n'
    ;;
  cheater)
    if [ -n "$fixtures" ] && [ -d "$fixtures/mock/cheat" ]; then
      cp -R "$fixtures/mock/cheat"/. .
      printf 'mock-agent: overwrote test files from mock/cheat\n'
    else
      printf 'mock-agent: no mock/cheat directory; nothing overwritten\n'
    fi
    ;;
  *)
    printf 'mock-agent: unknown mode %%s\n' "$mode"
    ;;
esac
printf '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"mock-agent","model":"mock-agent","total_cost_usd":0.01,"usage":{"input_tokens":1000,"output_tokens":200}}\n'
`, mockMode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// ---------------------------------------------------------------------------
// Acceptance environment detection

type acceptanceEnv struct {
	PythonAvailable bool
	PytestAvailable bool
	PythonVersion   string
	Note            string
}

// detectAcceptanceEnv probes python3/pytest once per run. Missing interpreters
// are reported, not hidden: affected rows are marked acceptance_skipped and
// are never counted as passes.
func detectAcceptanceEnv(ctx context.Context, tasks []Task) acceptanceEnv {
	var env acceptanceEnv
	needsPython := false
	for _, t := range tasks {
		if t.Lang == "python" {
			needsPython = true
		}
	}
	if !needsPython {
		return env
	}
	if _, err := exec.LookPath("python3"); err != nil {
		env.Note = "python3 was not found in PATH: " + err.Error()
		return env
	}
	out, res := runCommand(ctx, 20*time.Second, "", nil, "python3", "--version")
	if res.ExitCode != 0 {
		env.Note = fmt.Sprintf("python3 --version failed (exit %d): %s", res.ExitCode, firstLine(out))
		return env
	}
	env.PythonAvailable = true
	env.PythonVersion = firstLine(out)
	pout, pres := runCommand(ctx, 30*time.Second, "", nil, "python3", "-m", "pytest", "--version")
	if pres.ExitCode != 0 {
		env.Note = fmt.Sprintf("python3 -m pytest --version failed (exit %d): %s", pres.ExitCode, firstLine(pout))
		return env
	}
	env.PytestAvailable = true
	return env
}
