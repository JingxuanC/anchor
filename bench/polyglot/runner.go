package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// runObservation executes one (task, trial, arm) observation. Setup failures are
// recorded on the row instead of aborting the run.
func runObservation(ctx context.Context, cfg Config, env *benchmarkEnv, task Task, trial int, arm string) TrialResult {
	started := time.Now()
	res := TrialResult{Task: task.Slug, Lang: task.Lang, Trial: trial, Arm: arm}
	logDir := filepath.Join(env.outDir, "logs", fmt.Sprintf("%s-t%02d-%s", task.Slug, trial, arm))
	res.LogDir = logDir
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		res.Errors = append(res.Errors, "create log dir: "+err.Error())
		res.WallMS = time.Since(started).Milliseconds()
		return res
	}

	workBase, err := os.MkdirTemp("", fmt.Sprintf("anchor-polyglot-%s-t%02d-%s-", task.Slug, trial, arm))
	if err != nil {
		res.Errors = append(res.Errors, "create work dir: "+err.Error())
		res.WallMS = time.Since(started).Milliseconds()
		return res
	}
	if cfg.KeepWork {
		res.WorkDir = workBase
	} else {
		defer func() { _ = os.RemoveAll(workBase) }()
	}
	proj := filepath.Join(workBase, "project")
	if err := copyProject(task.Dir, proj); err != nil {
		res.Errors = append(res.Errors, "copy exercise: "+err.Error())
		res.WallMS = time.Since(started).Milliseconds()
		return res
	}
	canonical, err := readCanonicalTestFiles(task)
	if err != nil {
		res.Errors = append(res.Errors, "read canonical tests: "+err.Error())
		res.WallMS = time.Since(started).Milliseconds()
		return res
	}

	if commit, err := gitInitAndCommit(ctx, proj, "initial exercise"); err != nil {
		res.Errors = append(res.Errors, err.Error())
	} else {
		res.FixtureCommit = commit
	}

	// ---------------------------------------------------------------- setup arm
	var anchorWorkspace anchorWorkspace
	if arm == armAnchor {
		var warnings []string
		anchorWorkspace, warnings = setupAnchorWorkspace(ctx, env.anchor, proj, task.Slug, filepath.Join(logDir, "anchor-setup.log"))
		res.Errors = append(res.Errors, warnings...)
		res.AnchorStateDir = anchorWorkspace.StateDir
		res.AnchorNodeID = anchorWorkspace.NodeID
		if anchorWorkspace.StateDir != "" {
			if _, err := gitCommitAll(ctx, proj, "anchor: attach workspace and create task node"); err != nil {
				res.Errors = append(res.Errors, "commit anchor setup: "+err.Error())
			}
		}
	}

	prompt := buildPrompt(task)
	if err := writeFile(filepath.Join(logDir, "prompt.txt"), prompt); err != nil {
		res.Errors = append(res.Errors, "write prompt: "+err.Error())
	}

	// ---------------------------------------------------------------- run agent
	preHashes := hashTestFiles(proj, task.TestFiles)
	childArgv, agentEnv, err := agentInvocation(cfg, task, logDir, prompt)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.WallMS = time.Since(started).Milliseconds()
		return res
	}

	var session *anchorRunSession
	if arm == armAnchor && anchorWorkspace.StateDir != "" {
		session = &anchorRunSession{
			Bin:         env.anchor.Bin,
			StateDir:    anchorWorkspace.StateDir,
			NodeID:      anchorWorkspace.NodeID,
			SessionID:   fmt.Sprintf("polyglot-%s-t%02d-%s", sanitizeNodeID(task.Slug), trial, arm),
			WrapSupport: env.anchor.WrapSupported,
		}
		session.ContextFile = ensureAnchorContext(ctx, env.anchor, session.StateDir, logDir, task.Slug)
	}

	run := runAgentProcess(ctx, cfg, env, proj, logDir, childArgv, agentEnv, session)
	res.AgentMS = run.Duration.Milliseconds()
	res.AgentExit = run.ExitCode
	res.AgentTimedOut = run.TimedOut
	res.WrapUsed = run.WrapUsed
	res.WrapFallback = run.WrapFallback
	res.Usage = run.Usage
	res.Tokens = run.Usage.Total()
	res.CostUSD = run.Usage.CostUSD
	res.AgentModel = run.Usage.Model
	if cfg.AgentModel != "" {
		res.AgentModel = cfg.AgentModel
	}

	// ------------------------------------------------------- capture patch/cheat
	// Stage everything so untracked files created by the agent appear in the
	// patch; the patch is a log artifact and is written before tests are
	// restored, so it shows exactly what the agent did.
	if _, err := gitCommand(ctx, proj, "add", "-A"); err == nil {
		if patch, err := gitCommand(ctx, proj, "diff", "--cached", "HEAD"); err == nil {
			res.PatchBytes = len(patch)
			res.PatchFiles = countDiffFiles(patch)
			if werr := writeFile(filepath.Join(logDir, "patch.diff"), patch); werr != nil {
				res.Errors = append(res.Errors, "write patch: "+werr.Error())
			}
		} else {
			res.Errors = append(res.Errors, err.Error())
		}
	} else {
		res.Errors = append(res.Errors, err.Error())
	}

	postHashes := hashTestFiles(proj, task.TestFiles)
	flags := integrityFlags(task, canonical, proj)
	if flags == nil {
		flags = []string{}
	}
	res.CheatFlags = flags
	res.TestsModified = len(flags) > 0
	removeUnexpectedTestFiles(task, proj)
	if err := writeFile(filepath.Join(logDir, "test-integrity.txt"), renderIntegrityReport(task, preHashes, postHashes, flags)); err != nil {
		res.Errors = append(res.Errors, "write test integrity log: "+err.Error())
	}

	// Restore the canonical tests before grading. The anti-cheat check above
	// records what changed; grading must always use pristine tests.
	if err := restoreCanonicalTestFiles(task, proj); err != nil {
		res.Errors = append(res.Errors, "restore canonical tests: "+err.Error())
	}

	// ---------------------------------------------------------------- grading
	accept := runAcceptance(ctx, cfg, env, task, proj, filepath.Join(logDir, "acceptance.log"))
	res.AcceptanceSkipped = accept.Skipped
	res.AcceptanceSkipReason = accept.SkipReason
	res.AcceptExit = accept.ExitCode
	res.AcceptMS = accept.Duration.Milliseconds()
	res.Pass = accept.Pass
	if !accept.Pass && !accept.Skipped && res.AcceptExit == 0 {
		res.Errors = append(res.Errors, "acceptance exited 0 but was not counted as a pass")
	}

	res.WallMS = time.Since(started).Milliseconds()
	if res.WallMS < res.AgentMS+res.AcceptMS {
		res.WallMS = res.AgentMS + res.AcceptMS
	}
	return res
}

// anchorRunSession carries the anchor-arm environment for one observation.
type anchorRunSession struct {
	Bin         string
	StateDir    string
	NodeID      string
	SessionID   string
	ContextFile string
	WrapSupport bool
}

// agentInvocation builds the agent argv and extra environment for one
// observation. Mock runs use a generated shell script; real runs append the
// prompt as the final argument.
func agentInvocation(cfg Config, task Task, logDir, prompt string) ([]string, map[string]string, error) {
	if cfg.MockAgent {
		script, err := writeMockAgentScript(logDir, task, cfg.MockMode)
		if err != nil {
			return nil, nil, fmt.Errorf("write mock agent script: %w", err)
		}
		return []string{"sh", script}, map[string]string{
			"POLY_MOCK_FIXTURES": task.Dir,
			"POLY_MOCK_MODE":     cfg.MockMode,
			"POLY_SLUG":          task.Slug,
		}, nil
	}
	argv, err := splitCommand(cfg.AgentCmd)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid --agent-cmd: %w", err)
	}
	argv = append(argv, prompt)
	return argv, map[string]string{}, nil
}

func runAgentProcess(ctx context.Context, cfg Config, env *benchmarkEnv, proj, logDir string, childArgv []string, agentEnv map[string]string, session *anchorRunSession) agentRunResult {
	logPath := filepath.Join(logDir, "agent.log")
	if session != nil && session.WrapSupport {
		wrapArgv := []string{session.Bin, "wrap", "--dir", session.StateDir, "--node", session.NodeID, "--"}
		wrapArgv = append(wrapArgv, childArgv...)
		wrapEnv := copyStringMap(agentEnv)
		wrapEnv["ANCHOR_BIN"] = session.Bin
		run := execAgent(ctx, cfg, proj, logPath, wrapArgv, wrapEnv, "")
		if !shouldFallbackFromWrap(run.Output) {
			run.WrapUsed = true
			return run
		}
		run.WrapFallback = "anchor wrap failed, retried directly: " + firstLine(run.Output)
		// The wrap attempt may have written a partial log; the retry replaces it.
		return directAgentRun(ctx, cfg, proj, logPath, childArgv, agentEnv, session, run.WrapFallback)
	}
	return directAgentRun(ctx, cfg, proj, logPath, childArgv, agentEnv, session, "")
}

func directAgentRun(ctx context.Context, cfg Config, proj, logPath string, childArgv []string, agentEnv map[string]string, session *anchorRunSession, fallback string) agentRunResult {
	env := copyStringMap(agentEnv)
	if session != nil {
		env["ANCHOR_BIN"] = session.Bin
		env["ANCHOR_WORKSPACE"] = session.StateDir
		env["ANCHOR_NODE"] = session.NodeID
		env["ANCHOR_SESSION"] = session.SessionID
		if session.ContextFile != "" {
			env["ANCHOR_CONTEXT_FILE"] = session.ContextFile
		}
	}
	run := execAgent(ctx, cfg, proj, logPath, childArgv, env, "")
	run.WrapFallback = fallback
	return run
}

// shouldFallbackFromWrap reports whether the wrapped run failed because the
// binary does not support wrap or the node is unusable. A normal non-zero agent
// exit code is not a fallback signal: that is a real agent failure.
func shouldFallbackFromWrap(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"unknown command",
		"flag provided but not defined",
		"usage: anchor wrap",
		"not found in this workspace",
		"has no ownership lease event",
		"anchor binary was not found",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Prompt and project setup

func buildPrompt(task Task) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(task.Instructions))
	b.WriteString("\n\n---\n\n")
	b.WriteString("## Task\n\n")
	b.WriteString("The exercise is checked out in the current working directory. Implement it so that the acceptance tests pass.\n\n")
	fmt.Fprintf(&b, "- Language: %s\n", task.Lang)
	if len(task.SourceFiles) > 0 {
		fmt.Fprintf(&b, "- Source file(s) to implement: %s\n", strings.Join(task.SourceFiles, ", "))
	}
	fmt.Fprintf(&b, "- Acceptance command: %s\n", strings.Join(task.Acceptance, " "))
	fmt.Fprintf(&b, "- Test files: %s\n", strings.Join(task.TestFiles, ", "))
	b.WriteString("\nRules:\n\n")
	b.WriteString("- Do NOT modify, delete, disable or weaken the test files (or add test configuration that changes how they run).\n")
	b.WriteString("- The canonical test files are restored from the pristine exercise before grading, so editing them cannot help.\n")
	b.WriteString("- Only change the exercise's source files; keep the public interface the tests expect.\n")
	return b.String()
}

// copyProject copies one exercise into a fresh project directory. Harness-only
// and VCS/cache directories are skipped so the agent never sees mock/correct
// or the original git history.
func copyProject(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != src && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// ---------------------------------------------------------------------------
// Git helpers

func gitInitAndCommit(ctx context.Context, proj, message string) (string, error) {
	if out, res := runCommand(ctx, time.Minute, proj, fixedGitEnv(), "git", "init", "-q", "-b", "main"); res.ExitCode != 0 {
		return "", fmt.Errorf("git init: %s", firstLine(out))
	}
	for _, kv := range [][2]string{
		{"user.email", "polyglot-bench@example.com"},
		{"user.name", "Polyglot Benchmark"},
		{"commit.gpgsign", "false"},
	} {
		if out, res := runCommand(ctx, time.Minute, proj, fixedGitEnv(), "git", "config", kv[0], kv[1]); res.ExitCode != 0 {
			return "", fmt.Errorf("git config %s: %s", kv[0], firstLine(out))
		}
	}
	return gitCommitAll(ctx, proj, message)
}

func gitCommitAll(ctx context.Context, proj, message string) (string, error) {
	if out, res := runCommand(ctx, time.Minute, proj, fixedGitEnv(), "git", "add", "-A"); res.ExitCode != 0 {
		return "", fmt.Errorf("git add: %s", firstLine(out))
	}
	if out, res := runCommand(ctx, time.Minute, proj, fixedGitEnv(), "git", "commit", "-q", "--allow-empty", "-m", message); res.ExitCode != 0 {
		return "", fmt.Errorf("git commit: %s", firstLine(out))
	}
	out, res := runCommand(ctx, time.Minute, proj, fixedGitEnv(), "git", "rev-parse", "HEAD")
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git rev-parse: %s", firstLine(out))
	}
	return strings.TrimSpace(out), nil
}

func gitCommand(ctx context.Context, proj string, args ...string) (string, error) {
	out, res := runCommand(ctx, time.Minute, proj, fixedGitEnv(), "git", args...)
	if res.ExitCode != 0 {
		return out, fmt.Errorf("git %s: %s", strings.Join(args, " "), firstLine(out))
	}
	return out, nil
}

func fixedGitEnv() map[string]string {
	return map[string]string{
		"GIT_AUTHOR_DATE":     "2024-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE":  "2024-01-01T00:00:00Z",
		"GIT_CONFIG_NOSYSTEM": "1",
	}
}

func countDiffFiles(patch string) int {
	n := 0
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Anti-cheat: canonical test files

// readCanonicalTestFiles snapshots the pristine test files of one exercise.
func readCanonicalTestFiles(task Task) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range task.TestFiles {
		data, err := os.ReadFile(filepath.Join(task.Dir, name))
		if err != nil {
			return nil, fmt.Errorf("read canonical test %s: %w", name, err)
		}
		files[name] = data
	}
	return files, nil
}

// restoreCanonicalTestFiles writes the pristine test file contents back into the
// project, recreating deleted files. Grading always happens after this.
func restoreCanonicalTestFiles(task Task, proj string) error {
	for _, name := range task.TestFiles {
		data, err := os.ReadFile(filepath.Join(task.Dir, name))
		if err != nil {
			return fmt.Errorf("read canonical test %s: %w", name, err)
		}
		target := filepath.Join(proj, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("restore %s: %w", name, err)
		}
	}
	return nil
}

// hashTestFiles returns the SHA-256 of each canonical test file as it currently
// exists in proj. A missing file is recorded as "missing".
func hashTestFiles(proj string, names []string) map[string]string {
	hashes := map[string]string{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(proj, name))
		if err != nil {
			hashes[name] = "missing"
			continue
		}
		hashes[name] = sha256Hex(data)
	}
	return hashes
}

// integrityFlags compares the project's test files to the canonical snapshot and
// flags unexpected test infrastructure files. Returned values are stable
// machine-readable strings.
func integrityFlags(task Task, canonical map[string][]byte, proj string) []string {
	var flags []string
	for _, name := range task.TestFiles {
		current, err := os.ReadFile(filepath.Join(proj, name))
		if err != nil {
			flags = append(flags, "test_file_deleted:"+name)
			continue
		}
		canonicalData, ok := canonical[name]
		if !ok || !bytesEqual(current, canonicalData) {
			flags = append(flags, "test_file_modified:"+name)
		}
	}
	for _, name := range unexpectedTestFiles(task, proj) {
		flags = append(flags, "unexpected_test_file:"+name)
	}
	sort.Strings(flags)
	return flags
}

// unexpectedTestFiles lists test infrastructure files in proj that were not part
// of the pristine exercise.
func unexpectedTestFiles(task Task, proj string) []string {
	pristine := map[string]bool{}
	if entries, err := os.ReadDir(task.Dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				pristine[e.Name()] = true
			}
		}
	}
	entries, err := os.ReadDir(proj)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || pristine[e.Name()] {
			continue
		}
		if isTestInfrastructure(task.Lang, e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func isTestInfrastructure(lang, name string) bool {
	switch lang {
	case "python":
		switch name {
		case "conftest.py", "pytest.ini", "tox.ini", "setup.cfg", "pyproject.toml", "setup.py":
			return true
		}
		return strings.HasSuffix(name, "_test.py") || (strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py"))
	case "go":
		return strings.HasSuffix(name, "_test.go")
	}
	return false
}

// removeUnexpectedTestFiles deletes test infrastructure the agent added, so it
// cannot influence grading. Canonical test files are restored afterwards.
func removeUnexpectedTestFiles(task Task, proj string) {
	for _, name := range unexpectedTestFiles(task, proj) {
		_ = os.Remove(filepath.Join(proj, name))
	}
}

func renderIntegrityReport(task Task, pre, post map[string]string, flags []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# test integrity for %s (%s)\n\n", task.Slug, task.Lang)
	fmt.Fprintf(&b, "%-40s %-16s %-16s\n", "test file", "before agent", "after agent")
	for _, name := range task.TestFiles {
		fmt.Fprintf(&b, "%-40s %-16s %-16s\n", name, pre[name], post[name])
	}
	b.WriteString("\ncheat flags:\n")
	if len(flags) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, f := range flags {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	return b.String()
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Acceptance

type acceptanceResult struct {
	Pass       bool
	ExitCode   int
	Duration   time.Duration
	Skipped    bool
	SkipReason string
}

func runAcceptance(ctx context.Context, cfg Config, env *benchmarkEnv, task Task, proj, logPath string) acceptanceResult {
	if task.Lang == "python" {
		switch {
		case !env.acceptance.PythonAvailable:
			reason := env.acceptance.Note
			if reason == "" {
				reason = "python3 is not available"
			}
			return writeSkippedAcceptance(logPath, task, "python3 unavailable: "+reason)
		case !env.acceptance.PytestAvailable:
			reason := env.acceptance.Note
			if reason == "" {
				reason = "pytest is not importable"
			}
			return writeSkippedAcceptance(logPath, task, "pytest unavailable: "+reason)
		}
	}

	extraEnv := map[string]string{}
	if task.Lang == "go" {
		extraEnv["GOPROXY"] = "off"
		extraEnv["GOTOOLCHAIN"] = "local"
		extraEnv["GO111MODULE"] = "on"
	}
	if task.Lang == "python" {
		extraEnv["PYTHONDONTWRITEBYTECODE"] = "1"
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return acceptanceResult{ExitCode: -1, SkipReason: "create acceptance log: " + err.Error()}
	}
	defer func() { _ = logFile.Close() }()
	fmt.Fprintf(logFile, "# command: %s\n# cwd: %s\n# timeout: %s\n\n", strings.Join(task.Acceptance, " "), proj, cfg.Timeout)
	res := runManaged(ctx, cfg.Timeout, proj, mergeEnv(os.Environ(), extraEnv), logFile, task.Acceptance)
	fmt.Fprintf(logFile, "\n# exit: %d timed_out=%v duration_ms=%d\n", res.ExitCode, res.TimedOut, res.Duration.Milliseconds())
	accept := acceptanceResult{ExitCode: res.ExitCode, Duration: res.Duration}
	accept.Pass = res.ExitCode == 0
	return accept
}

func writeSkippedAcceptance(logPath string, task Task, reason string) acceptanceResult {
	_ = writeFile(logPath, fmt.Sprintf("# command: %s\n# SKIPPED: %s\n\nAcceptance was not run, so this row is NOT a pass.\n",
		strings.Join(task.Acceptance, " "), reason))
	return acceptanceResult{ExitCode: -1, Skipped: true, SkipReason: reason}
}

// ---------------------------------------------------------------------------
// Small helpers

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
