package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requirePytest skips a test when python3/pytest are unavailable. These tests
// exercise real acceptance runs; CI images without pytest should not report a
// harness failure.
func requirePytest(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; skipping Python acceptance test")
	}
	if _, res := runCommand(context.Background(), 30*time.Second, "", nil, "python3", "-m", "pytest", "--version"); res.ExitCode != 0 {
		t.Skip("pytest not available; skipping Python acceptance test")
	}
}

func baseMockConfig(t *testing.T, outDir string) Config {
	t.Helper()
	return Config{
		PolyglotDir: fixturesDir(t),
		Fixtures:    fixturesDir(t),
		Tasks:       "all",
		Trials:      1,
		Arm:         armBaseline,
		MockAgent:   true,
		MockMode:    mockModeNormal,
		Timeout:     2 * time.Minute,
		OutDir:      outDir,
	}
}

func runBench(t *testing.T, cfg Config) *Report {
	t.Helper()
	report, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Skipped {
		t.Fatalf("run skipped: %s", report.SkipReason)
	}
	return report
}

func rowsFor(report *Report, task, arm string) []TrialResult {
	var out []TrialResult
	for _, row := range report.Trials {
		if (task == "" || row.Task == task) && (arm == "" || row.Arm == arm) {
			out = append(out, row)
		}
	}
	return out
}

func readLogFile(t *testing.T, row TrialResult, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(row.LogDir, name))
	if err != nil {
		t.Fatalf("read %s log for %s/%s: %v", name, row.Task, row.Arm, err)
	}
	return string(data)
}

func TestParseFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"--mock-agent", "--polyglot-dir", "tasks", "--trials", "2", "--arm", "anchor",
		"--lang", "Python", "--tasks", "leap", "--max-usd", "1.5", "--max-tokens", "1000"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.PolyglotDir != "tasks" || cfg.Trials != 2 || cfg.Arm != armAnchor || cfg.Lang != "python" {
		t.Fatalf("parsed config = %+v", cfg)
	}
	if cfg.Fixtures != defaultFixtures {
		t.Fatalf("default fixtures = %q, want %q", cfg.Fixtures, defaultFixtures)
	}
	if cfg.MockMode != mockModeNormal {
		t.Fatalf("default mock mode = %q", cfg.MockMode)
	}
	if cfg.MaxUSD != 1.5 || cfg.MaxTokens != 1000 {
		t.Fatalf("budget flags = %v/%v", cfg.MaxUSD, cfg.MaxTokens)
	}

	bad := [][]string{
		{"--mock-agent", "--agent-cmd", "claude -p"},
		{"--trials", "0"},
		{"--mock-agent", "--arm", "nope"},
		{"--mock-agent", "--mock-mode", "chaos"},
		{"--mock-agent", "--max-usd", "-1"},
		{"--mock-agent", "--max-tokens", "-1"},
		{"--mock-agent", "--timeout", "0s"},
		{},
	}
	for _, args := range bad {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("parseFlags(%v) unexpectedly succeeded", args)
		}
	}
}

func TestMockNormalPassesBaseline(t *testing.T) {
	requirePytest(t)
	outDir := t.TempDir()
	report := runBench(t, baseMockConfig(t, outDir))

	if report.SchemaVersion != schemaVersion {
		t.Fatalf("schema version = %d", report.SchemaVersion)
	}
	if len(report.Trials) != 2 {
		t.Fatalf("got %d trial rows, want 2", len(report.Trials))
	}
	for _, row := range rowsFor(report, "", armBaseline) {
		if !row.Pass {
			t.Fatalf("%s/%s did not pass: errors=%v accept_exit=%d skip=%v", row.Task, row.Arm, row.Errors, row.AcceptExit, row.AcceptanceSkipped)
		}
		if row.TestsModified || len(row.CheatFlags) != 0 {
			t.Fatalf("%s should not be flagged as cheating: %+v", row.Task, row.CheatFlags)
		}
		if row.AgentExit != 0 || row.AcceptExit != 0 {
			t.Fatalf("%s exits = agent %d accept %d", row.Task, row.AgentExit, row.AcceptExit)
		}
		if row.Tokens != 1200 || row.CostUSD != 0.01 {
			t.Fatalf("%s usage = %d tokens / %v USD, want 1200/0.01 from the mock JSON result", row.Task, row.Tokens, row.CostUSD)
		}
		if row.WallMS <= 0 || row.AgentMS <= 0 || row.AcceptMS <= 0 {
			t.Fatalf("%s timings = wall %d agent %d accept %d", row.Task, row.WallMS, row.AgentMS, row.AcceptMS)
		}
		for _, name := range []string{"prompt.txt", "patch.diff", "agent.log", "acceptance.log", "test-integrity.txt"} {
			if data := readLogFile(t, row, name); data == "" {
				t.Fatalf("%s missing artifact %s", row.Task, name)
			}
		}
		if !strings.Contains(readLogFile(t, row, "prompt.txt"), "Do NOT modify") {
			t.Fatalf("%s prompt does not state the no-test-modification rule", row.Task)
		}
	}

	base := report.Aggregates[armBaseline]
	if base.Tasks != 2 || base.Trials != 2 || base.Pass1 != 1 || base.PassK != 1 {
		t.Fatalf("baseline aggregate = %+v, want 2 tasks/2 trials/pass@1=1/pass@k=1", base)
	}
	if base.Pass1CI.Low < 0 || base.Pass1CI.High > 1 || base.Pass1CI.Low > base.Pass1CI.High {
		t.Fatalf("Wilson CI out of range: %+v", base.Pass1CI)
	}
	if len(report.PerTask) != 2 {
		t.Fatalf("per-task rows = %d, want 2", len(report.PerTask))
	}
	for _, task := range report.PerTask {
		stats, ok := task.Arms[armBaseline]
		if !ok || stats.Pass1 != 1 || !stats.PassK || stats.TestsModified != 0 {
			t.Fatalf("per-task stats for %s = %+v", task.Task, stats)
		}
	}
	if len(report.Metadata.Tasks) != 2 || report.Metadata.Tasks[0].Hash == "" {
		t.Fatalf("task metadata = %+v", report.Metadata.Tasks)
	}
	if report.Metadata.PytestAvailable != true {
		t.Fatalf("metadata should record pytest availability: %+v", report.Metadata)
	}

	// Artifacts are written and parseable.
	for _, path := range []string{report.Artifacts.JSON, report.Artifacts.Markdown} {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			t.Fatalf("artifact %s missing: %v", path, err)
		}
	}
	raw, err := os.ReadFile(report.Artifacts.JSON)
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("report.json does not round-trip: %v", err)
	}
	if decoded.SchemaVersion != schemaVersion || len(decoded.Trials) != 2 {
		t.Fatalf("decoded report = schema %d, %d rows", decoded.SchemaVersion, len(decoded.Trials))
	}
	md := string(mustRead(t, report.Artifacts.Markdown))
	for _, want := range []string{"# Aider-polyglot benchmark report", "## Results", "## Per-task results", "## Budget", "## Environment and honest caveats", "pass@1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("report.md is missing %q", want)
		}
	}
}

func TestMockSabotageFails(t *testing.T) {
	requirePytest(t)
	cfg := baseMockConfig(t, t.TempDir())
	cfg.MockMode = mockModeSabotage
	report := runBench(t, cfg)
	rows := rowsFor(report, "", armBaseline)
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	for _, row := range rows {
		if row.Pass {
			t.Fatalf("%s passed in sabotage mode", row.Task)
		}
		if row.TestsModified {
			t.Fatalf("%s was flagged for cheating in sabotage mode", row.Task)
		}
		if row.AgentExit != 0 {
			t.Fatalf("%s agent exit = %d, want 0 (sabotage is a no-op)", row.Task, row.AgentExit)
		}
		if row.AcceptExit == 0 || row.AcceptanceSkipped {
			t.Fatalf("%s acceptance = exit %d skipped=%v, want a real failure", row.Task, row.AcceptExit, row.AcceptanceSkipped)
		}
	}
	if report.Aggregates[armBaseline].Pass1 != 0 {
		t.Fatalf("sabotage pass@1 = %v, want 0", report.Aggregates[armBaseline].Pass1)
	}
}

func TestMockCheaterFlaggedAndRestored(t *testing.T) {
	requirePytest(t)
	cfg := baseMockConfig(t, t.TempDir())
	cfg.MockMode = mockModeCheater
	report := runBench(t, cfg)
	rows := rowsFor(report, "", armBaseline)
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	for _, row := range rows {
		if !row.TestsModified {
			t.Fatalf("%s should be flagged TestsModified", row.Task)
		}
		wantFlag := "test_file_modified:" + row.Task + "_test.py"
		if !containsString(row.CheatFlags, wantFlag) {
			t.Fatalf("%s cheat flags = %v, want %q", row.Task, row.CheatFlags, wantFlag)
		}
		if row.Pass {
			t.Fatalf("%s passed after cheating; restoration failed", row.Task)
		}
		patch := readLogFile(t, row, "patch.diff")
		if !strings.Contains(patch, "test_placeholder") {
			t.Fatalf("%s patch does not record the overwritten test file: %s", row.Task, truncate(patch, 400))
		}
		integrity := readLogFile(t, row, "test-integrity.txt")
		if !strings.Contains(integrity, wantFlag) {
			t.Fatalf("%s integrity log missing %q: %s", row.Task, wantFlag, integrity)
		}
		acceptance := readLogFile(t, row, "acceptance.log")
		if !strings.Contains(acceptance, "failed") && !strings.Contains(acceptance, "error") {
			t.Fatalf("%s acceptance log should show canonical test failures: %s", row.Task, acceptance)
		}
	}
	if report.Aggregates[armBaseline].Pass1 != 0 {
		t.Fatalf("cheater pass@1 = %v, want 0", report.Aggregates[armBaseline].Pass1)
	}
	if report.Aggregates[armBaseline].TestsModified != 2 {
		t.Fatalf("cheater tests_modified count = %d, want 2", report.Aggregates[armBaseline].TestsModified)
	}
}

func TestRestoreCanonicalTests(t *testing.T) {
	tasks, _, err := loadTasks(fixturesDir(t), "python", "leap")
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	task := tasks[0]
	proj := t.TempDir()
	if err := copyProject(task.Dir, proj); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	canonical := mustRead(t, filepath.Join(task.Dir, task.TestFiles[0]))
	testPath := filepath.Join(proj, task.TestFiles[0])
	testFile := task.TestFiles[0]

	// Not modified yet.
	if flags := integrityFlags(task, map[string][]byte{testFile: canonical}, proj); len(flags) != 0 {
		t.Fatalf("pristine project flags = %v", flags)
	}
	// Detection sees a modified test file...
	if err := os.WriteFile(testPath, []byte("def test_cheat():\n    assert True\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := integrityFlags(task, map[string][]byte{testFile: canonical}, proj)
	if len(flags) != 1 || flags[0] != "test_file_modified:"+testFile {
		t.Fatalf("modified flags = %v", flags)
	}
	// ...and restoration puts the canonical bytes back.
	if err := restoreCanonicalTestFiles(task, proj); err != nil {
		t.Fatalf("restoreCanonicalTestFiles: %v", err)
	}
	if got := mustRead(t, testPath); string(got) != string(canonical) {
		t.Fatalf("restored content differs from canonical")
	}
	if flags := integrityFlags(task, map[string][]byte{testFile: canonical}, proj); len(flags) != 0 {
		t.Fatalf("post-restore flags = %v", flags)
	}

	// A deleted test file is flagged and restored.
	if err := os.Remove(testPath); err != nil {
		t.Fatal(err)
	}
	if flags := integrityFlags(task, map[string][]byte{testFile: canonical}, proj); len(flags) != 1 || flags[0] != "test_file_deleted:"+testFile {
		t.Fatalf("deleted flags = %v", flags)
	}
	if err := restoreCanonicalTestFiles(task, proj); err != nil {
		t.Fatalf("restore after delete: %v", err)
	}
	if _, err := os.Stat(testPath); err != nil {
		t.Fatalf("test file was not recreated: %v", err)
	}
}

func TestUnexpectedTestInfrastructureRemoved(t *testing.T) {
	tasks, _, err := loadTasks(fixturesDir(t), "python", "two_fer")
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	task := tasks[0]
	proj := t.TempDir()
	if err := copyProject(task.Dir, proj); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "conftest.py"), []byte("import sys\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonical := map[string][]byte{}
	for _, name := range task.TestFiles {
		canonical[name] = mustRead(t, filepath.Join(task.Dir, name))
	}
	flags := integrityFlags(task, canonical, proj)
	if !containsString(flags, "unexpected_test_file:conftest.py") {
		t.Fatalf("flags = %v, want unexpected conftest.py", flags)
	}
	removeUnexpectedTestFiles(task, proj)
	if _, err := os.Stat(filepath.Join(proj, "conftest.py")); !os.IsNotExist(err) {
		t.Fatalf("conftest.py should have been removed, stat err = %v", err)
	}
}

func TestBudgetTruncation(t *testing.T) {
	requirePytest(t)
	cfg := baseMockConfig(t, t.TempDir())
	cfg.Trials = 3
	cfg.MaxUSD = 0.001 // the mock reports 0.01 per observation
	report := runBench(t, cfg)
	if !report.Budget.BudgetExceeded || !report.Budget.Truncated {
		t.Fatalf("budget = %+v, want exceeded+truncated", report.Budget)
	}
	if len(report.Trials) != 1 {
		t.Fatalf("truncated run executed %d rows, want 1", len(report.Trials))
	}
	if report.Budget.Reason == "" {
		t.Fatal("truncated run should record a reason")
	}
	if report.Budget.TotalUSD < report.Budget.MaxUSD {
		t.Fatalf("total cost %v < cap %v", report.Budget.TotalUSD, report.Budget.MaxUSD)
	}
	// no task has a full set of 3 trials, so pass@k has no eligible tasks
	for _, agg := range report.Aggregates {
		if agg.PassKEligible != 0 || agg.PassK != 0 {
			t.Fatalf("aggregate %s: pass@k eligible=%d pass@k=%v, want 0", agg.Arm, agg.PassKEligible, agg.PassK)
		}
	}

	cfg = baseMockConfig(t, t.TempDir())
	cfg.Trials = 2
	cfg.MaxTokens = 100
	report = runBench(t, cfg)
	if !report.Budget.BudgetExceeded || len(report.Trials) != 1 {
		t.Fatalf("token cap run: budget=%+v rows=%d, want exceeded and 1 row", report.Budget, len(report.Trials))
	}
}

func TestAnchorArmFallsBackWhenBinaryUnavailable(t *testing.T) {
	requirePytest(t)
	if runtime.GOOS == "windows" {
		t.Skip("test uses /bin/false")
	}
	cfg := baseMockConfig(t, t.TempDir())
	cfg.Arm = armAnchor
	cfg.AnchorBin = "/bin/false"
	report := runBench(t, cfg)
	if report.Metadata.AnchorAvailable {
		t.Fatal("metadata should record the anchor binary as unavailable")
	}
	rows := rowsFor(report, "", armAnchor)
	if len(rows) != 2 {
		t.Fatalf("got %d anchor rows", len(rows))
	}
	for _, row := range rows {
		if row.WrapUsed {
			t.Fatalf("%s: wrap should not be used with an unusable anchor binary", row.Task)
		}
		if !row.Pass {
			t.Fatalf("%s should still pass via the direct fallback: errors=%v", row.Task, row.Errors)
		}
	}
}

func TestAnchorArmUsesWrap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the anchor binary")
	}
	requirePytest(t)
	// The arm is only checked when the local anchor binary can be built quickly.
	// Building is the same thing --arm anchor does in production.
	cfg := baseMockConfig(t, t.TempDir())
	cfg.Arm = armAnchor
	report := runBench(t, cfg)
	if !report.Metadata.AnchorAvailable {
		t.Fatalf("anchor unavailable: %s", report.Metadata.AnchorError)
	}
	if !report.Metadata.AnchorWrapSupported {
		t.Skip("local anchor binary has no wrap command")
	}
	for _, row := range rowsFor(report, "", armAnchor) {
		if !row.WrapUsed {
			t.Fatalf("%s did not use anchor wrap: fallback=%q", row.Task, row.WrapFallback)
		}
		if !row.Pass {
			t.Fatalf("%s did not pass: errors=%v", row.Task, row.Errors)
		}
	}
}

func TestReportLevelFilters(t *testing.T) {
	requirePytest(t)
	cfg := baseMockConfig(t, t.TempDir())
	cfg.Tasks = "two_fer"
	report := runBench(t, cfg)
	if report.Metadata.TaskCount != 1 || len(report.Trials) != 1 || report.Trials[0].Task != "two_fer" {
		t.Fatalf("task filter run = %d tasks, %d rows", report.Metadata.TaskCount, len(report.Trials))
	}
	if report.Metadata.Tasks[0].Slug != "two_fer" {
		t.Fatalf("metadata task = %q", report.Metadata.Tasks[0].Slug)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestBudgetCapReachedByLastObservationIsNotTruncation(t *testing.T) {
	requirePytest(t)
	cfg := baseMockConfig(t, t.TempDir())
	cfg.MaxUSD = 0.02 // two mock observations at 0.01 each
	report := runBench(t, cfg)
	if len(report.Trials) != 2 {
		t.Fatalf("rows = %d, want 2 (the cap is reached by the last observation)", len(report.Trials))
	}
	if !report.Budget.BudgetExceeded {
		t.Fatalf("budget = %+v, want budget_exceeded", report.Budget)
	}
	if report.Budget.Truncated {
		t.Fatalf("budget = %+v, want truncated=false when no planned work was skipped", report.Budget)
	}
}

func TestRealAgentCommandPath(t *testing.T) {
	requirePytest(t)
	fixtures := fixturesDir(t)
	scriptPath := filepath.Join(t.TempDir(), "fake-agent.sh")
	script := "#!/bin/sh\n" +
		"set -u\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in\n" +
		"  *\"Do NOT modify\"*) ;;\n" +
		"  *) echo \"prompt was not appended as the final argument\"; exit 9 ;;\n" +
		"esac\n" +
		"cp '" + fixtures + "/python/leap/mock/correct/leap.py' leap.py\n" +
		"printf '{\"type\":\"result\",\"usage\":{\"input_tokens\":5,\"output_tokens\":5},\"total_cost_usd\":0.001}\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		PolyglotDir: fixtures,
		Tasks:       "leap",
		Trials:      1,
		Arm:         armBaseline,
		AgentCmd:    "sh " + scriptPath,
		Timeout:     2 * time.Minute,
		OutDir:      t.TempDir(),
	}
	report := runBench(t, cfg)
	if len(report.Trials) != 1 {
		t.Fatalf("rows = %d", len(report.Trials))
	}
	row := report.Trials[0]
	if !row.Pass {
		t.Fatalf("real-agent-shaped run did not pass: errors=%v agent_exit=%d", row.Errors, row.AgentExit)
	}
	if row.AgentExit != 0 {
		t.Fatalf("agent exit = %d", row.AgentExit)
	}
	if row.Tokens != 10 || row.CostUSD != 0.001 || !row.Usage.Parsed {
		t.Fatalf("usage = %+v (tokens %d cost %v)", row.Usage, row.Tokens, row.CostUSD)
	}
	if report.Config.AgentCmd == "" || report.Metadata.AgentModel != "" {
		t.Fatalf("config/metadata agent fields = %+v / %+v", report.Config, report.Metadata)
	}
}

func TestAcceptanceSkippedWhenPytestMissing(t *testing.T) {
	tasks, _, err := loadTasks(fixturesDir(t), "python", "leap")
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	task := tasks[0]
	proj := t.TempDir()
	if err := copyProject(task.Dir, proj); err != nil {
		t.Fatalf("copyProject: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "acceptance.log")

	// pytest present in the environment but missing for this run.
	env := &benchmarkEnv{acceptance: acceptanceEnv{PythonAvailable: true, PytestAvailable: false, Note: "pytest is not importable"}}
	res := runAcceptance(context.Background(), Config{Timeout: time.Minute}, env, task, proj, logPath)
	if !res.Skipped || res.Pass {
		t.Fatalf("missing pytest: result = %+v, want skipped and not passed", res)
	}
	if !strings.Contains(res.SkipReason, "pytest") {
		t.Fatalf("skip reason = %q", res.SkipReason)
	}
	if log, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(log), "SKIPPED") {
		t.Fatalf("acceptance log = %q, err %v", string(log), err)
	}

	// python3 itself missing.
	env = &benchmarkEnv{acceptance: acceptanceEnv{Note: "python3 not found"}}
	res = runAcceptance(context.Background(), Config{Timeout: time.Minute}, env, task, proj, logPath)
	if !res.Skipped || res.Pass || !strings.Contains(res.SkipReason, "python3") {
		t.Fatalf("missing python3: result = %+v", res)
	}
}
