package main

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as the mock-agent entry point when the harness re-executes
// the test binary as the scripted agent. That makes mock mode a genuine fresh
// process during `go test` too, without compiling a helper binary.
func TestMain(m *testing.M) {
	if os.Getenv(envMockExec) == "1" {
		os.Exit(runMockAgent())
	}
	os.Exit(m.Run())
}

var (
	anchorBuildOnce sync.Once
	anchorBuildPath string
	anchorBuildErr  error
)

// testAnchorBin builds the anchor CLI once per test binary and reuses it.
func testAnchorBin(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("STRESS_TEST_ANCHOR_BIN")); p != "" {
		return p
	}
	anchorBuildOnce.Do(func() {
		root, err := findModuleRoot()
		if err != nil || root == "" {
			anchorBuildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "stress-anchor-bin-")
		if err != nil {
			anchorBuildErr = err
			return
		}
		out := filepath.Join(dir, "anchor")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/anchor")
		cmd.Dir = root
		if combined, err := cmd.CombinedOutput(); err != nil {
			anchorBuildErr = &testError{msg: "go build anchor: " + err.Error() + ": " + string(combined)}
			return
		}
		anchorBuildPath = out
	})
	if anchorBuildErr != nil {
		t.Fatalf("cannot build anchor for tests: %v", anchorBuildErr)
	}
	return anchorBuildPath
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func fixturesRoot(t *testing.T) string {
	t.Helper()
	root, err := findModuleRoot()
	if err != nil || root == "" {
		t.Fatalf("cannot locate module root: %v", err)
	}
	return filepath.Join(root, "bench", "fixtures", "stress")
}

// baseConfig returns a mock-agent configuration rooted in a temp output dir.
func baseConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Trials:      1,
		Arms:        []string{armBaseline, armRules, armPacket, armFull},
		MockAgent:   true,
		MockModes:   []string{mockModeNormal},
		Timeout:     3 * time.Minute,
		OutDir:      t.TempDir(),
		KeepWork:    false,
		FixturesDir: fixturesRoot(t),
		AnchorBin:   testAnchorBin(t),
		GoBin:       "go",
	}
}

func runForTest(t *testing.T, cfg *Config) *Report {
	t.Helper()
	report, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report == nil {
		t.Fatal("Run returned a nil report")
	}
	return report
}

func agg(t *testing.T, report *Report, arm string) *ArmSummary {
	t.Helper()
	a := report.Aggregates[arm]
	if a == nil {
		t.Fatalf("missing aggregate for arm %q", arm)
	}
	return a
}

// TestMockDiscrimination is the core self-test: with the scripted agent, a
// fresh baseline phase 2 must not pass, the packet/full arms must pass, and the
// ablation ordering packet >= rules >= baseline must hold.
func TestMockDiscrimination(t *testing.T) {
	cfg := baseConfig(t)
	report := runForTest(t, cfg)

	if report.SchemaVersion != schemaVersion || report.Suite != suiteName || report.SuiteVersion != suiteVersion {
		t.Fatalf("unexpected report header: %+v", report)
	}
	if len(report.Tasks) != 3 {
		t.Fatalf("got %d task metadata entries, want 3", len(report.Tasks))
	}
	if report.Metadata.GoVersion == "" || report.Config.AnchorVersion == "" {
		t.Fatalf("report is missing go/anchor version metadata: %+v", report.Metadata)
	}
	if report.Config.AnchorVersion == "unknown" || report.Config.AnchorVersion == "" {
		t.Fatalf("anchor version was not captured: %q", report.Config.AnchorVersion)
	}
	for _, task := range report.Tasks {
		if len(task.Hash) != 64 || len(task.Variants) < 3 {
			t.Fatalf("task metadata incomplete for %s: %+v", task.Name, task)
		}
	}

	baseline := agg(t, report, armBaseline)
	full := agg(t, report, armFull)
	rules := agg(t, report, armRules)
	packet := agg(t, report, armPacket)

	// The baseline cannot recover a convention that phase 1 never persisted.
	if baseline.Phase2Pass != 0 {
		t.Fatalf("baseline phase-2 passed %d/%d trials; mock discrimination is broken", baseline.Phase2Pass, baseline.Trials)
	}
	if baseline.VariantConveyed != 0 {
		t.Fatalf("baseline reported variant_conveyed %d times, want 0", baseline.VariantConveyed)
	}
	if full.Phase2Pass != full.Trials || full.Trials != 3 {
		t.Fatalf("full arm phase-2 pass = %d/%d, want 3/3", full.Phase2Pass, full.Trials)
	}
	if full.Phase1Pass != full.Trials {
		t.Fatalf("full arm phase-1 pass = %d/%d, want all", full.Phase1Pass, full.Trials)
	}
	// Compliance: the mock records its decision through the anchor CLI itself.
	if full.DecisionsRecorded != full.Trials {
		t.Fatalf("full arm decisions_recorded = %d/%d", full.DecisionsRecorded, full.Trials)
	}
	if full.PacketDecisions < full.Trials {
		t.Fatalf("full arm packet_decisions = %d, want >= %d", full.PacketDecisions, full.Trials)
	}
	if full.VariantConveyed != full.Trials {
		t.Fatalf("full arm variant_conveyed = %d/%d", full.VariantConveyed, full.Trials)
	}
	if full.HandoffVerified != full.Trials {
		t.Fatalf("full arm handoff_verified = %d/%d", full.HandoffVerified, full.Trials)
	}
	for _, row := range report.Trials {
		if row.Arm == armFull {
			if row.HandoffPreparedBy != "agent_wrap" {
				t.Fatalf("normal-mode full trial prepared handoff via %q, want agent_wrap", row.HandoffPreparedBy)
			}
			if !row.HandoffAcked || !row.HandoffVerified {
				t.Fatalf("full trial did not ack+verify: acked=%v verified=%v", row.HandoffAcked, row.HandoffVerified)
			}
		}
	}

	// Ablation ordering: packet >= rules >= baseline, and full >= packet.
	if !(packet.Phase2PassAt1 >= rules.Phase2PassAt1 &&
		rules.Phase2PassAt1 >= baseline.Phase2PassAt1 &&
		full.Phase2PassAt1 >= packet.Phase2PassAt1) {
		t.Fatalf("ablation ordering violated: baseline=%.2f rules=%.2f packet=%.2f full=%.2f",
			baseline.Phase2PassAt1, rules.Phase2PassAt1, packet.Phase2PassAt1, full.Phase2PassAt1)
	}
	if rules.VariantConveyed != 0 {
		t.Fatalf("rules arm (no packet) conveyed the variant %d times; the wrap context file leaked it", rules.VariantConveyed)
	}
	if rules.DecisionsRecorded != rules.Trials {
		t.Fatalf("rules arm decisions_recorded = %d/%d; the mock should still record decisions", rules.DecisionsRecorded, rules.Trials)
	}

	// Aggregate sanity: Wilson CI, pass^k and per-task breakdown are populated.
	for _, arm := range report.Arms {
		a := agg(t, report, arm)
		if a.Phase2CI.Lower < 0 || a.Phase2CI.Upper > 1 || a.Phase2CI.Lower > a.Phase2CI.Upper+1e-9 {
			t.Fatalf("invalid CI for %s: %+v", arm, a.Phase2CI)
		}
		if a.PassK < 1 || a.Phase2PassK < 0 || a.Phase2PassK > 1 {
			t.Fatalf("invalid pass^k for %s: k=%d passk=%v", arm, a.PassK, a.Phase2PassK)
		}
		if a.MeanTokens <= 0 {
			t.Fatalf("arm %s has no token accounting", arm)
		}
	}
	for _, task := range []string{"money", "names", "scores"} {
		ts := report.PerTask[task]
		if ts.Arms[armBaseline] == nil || ts.Arms[armFull] == nil {
			t.Fatalf("missing per-task stats for %s: %+v", task, ts)
		}
		if ts.Arms[armBaseline].Phase2Pass != 0 {
			t.Fatalf("task %s baseline phase-2 passed", task)
		}
		if ts.Arms[armFull].Phase2Pass != 1 {
			t.Fatalf("task %s full phase-2 did not pass", task)
		}
	}

	// Report files and per-row artifacts must exist.
	for _, rel := range []string{"report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(report.Artifacts.Root, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	var fullRow TrialRow
	for _, row := range report.Trials {
		if row.Arm == armFull {
			fullRow = row
			break
		}
	}
	for _, key := range []string{"prompt_phase1", "prompt_phase2", "log_phase1_agent", "log_phase2_agent", "accept_phase2", "patch", "packet", "hidden_test"} {
		rel, ok := fullRow.Artifacts[key]
		if !ok {
			continue // patch is only written when the diff is non-empty
		}
		if _, err := os.Stat(filepath.Join(report.Artifacts.Root, rel)); err != nil {
			t.Fatalf("artifact %q (%s) missing: %v", key, rel, err)
		}
	}
}

// TestSabotageDetectedAndRestored checks test-edit detection and canonical
// restoration: a phase-1 agent that weakens the visible tests must not pass
// phase 1 after the canonical tests are restored.
func TestSabotageDetectedAndRestored(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Arms = []string{armBaseline}
	cfg.Tasks = []string{"money"}
	cfg.MockModes = []string{mockModeSabotage}
	report := runForTest(t, cfg)
	if len(report.Trials) != 1 {
		t.Fatalf("got %d trials, want 1", len(report.Trials))
	}
	row := report.Trials[0]
	if !row.Phase1TestsModified || !row.TestsModified {
		t.Fatalf("sabotage was not detected: %+v", row)
	}
	if row.Phase1Pass {
		t.Fatalf("phase 1 passed after the visible tests were weakened; canonical restore is not working")
	}

	// Direct check that canonical restoration rewrites the fixture bytes.
	fx := loadFixtureForTest(t, "money")
	project := t.TempDir()
	if err := copyTree(fx.RepoDir, project); err != nil {
		t.Fatal(err)
	}
	testName := fx.Conv.VisibleTests[0]
	if err := os.WriteFile(filepath.Join(project, testName), []byte("package money\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified, restored, err := restoreVisibleTests(project, fx)
	if err != nil {
		t.Fatal(err)
	}
	if !modified || len(restored) != 1 || restored[0] != testName {
		t.Fatalf("restoreVisibleTests = (%v, %v), want (true, [%s])", modified, restored, testName)
	}
	got, err := os.ReadFile(filepath.Join(project, testName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(fx.CanonicalTests[testName]) {
		t.Fatalf("canonical test was not restored")
	}
}

// TestCheaterDetectedAndCannotPass checks that tampering in phase 2 is detected
// and cannot buy a pass: the visible tests are restored and the hidden test for
// the variant still fails against the naive implementation.
func TestCheaterDetectedAndCannotPass(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Arms = []string{armBaseline}
	cfg.Tasks = []string{"names"}
	cfg.MockModes = []string{mockModeCheater}
	report := runForTest(t, cfg)
	row := report.Trials[0]
	if !row.Phase2TestsModified || !row.TestsModified {
		t.Fatalf("cheater was not detected: %+v", row)
	}
	if row.Phase2Pass {
		t.Fatalf("cheater passed phase 2; hidden test/canonical restore is not discriminating")
	}
	if !row.Phase1Pass {
		t.Fatalf("phase 1 should pass in cheater mode (only phase 2 cheats): %+v", row)
	}
}

// TestFullArmRefreshAfterTamper exercises the harness refresh path: sabotage
// leaves the working tree different from the packet, so ensureHandoff must
// re-prepare it before the full arm can ack/verify.
func TestFullArmRefreshAfterTamper(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Arms = []string{armFull}
	cfg.Tasks = []string{"scores"}
	cfg.MockModes = []string{mockModeSabotage}
	report := runForTest(t, cfg)
	row := report.Trials[0]
	if !row.Phase1TestsModified {
		t.Fatalf("sabotage was not detected: %+v", row)
	}
	if !row.HandoffPrepared {
		t.Fatalf("full arm has no handoff packet: %+v", row)
	}
	if !row.HandoffRefreshed || row.HandoffPreparedBy != "harness_refresh" {
		t.Fatalf("stale packet was not refreshed after canonical restore: prepared_by=%q refreshed=%v",
			row.HandoffPreparedBy, row.HandoffRefreshed)
	}
	if !row.HandoffAcked || !row.HandoffVerified {
		t.Fatalf("full arm could not ack/verify the refreshed packet: acked=%v verified=%v", row.HandoffAcked, row.HandoffVerified)
	}
	if row.PacketDecisions == 0 {
		t.Fatalf("refreshed packet carries no variant decision: %+v", row)
	}
}

// TestHarnessDoesNotSeedDecisions proves the methodological invariant: the
// harness workspace contains only the goal and the two task nodes. Every
// decision must come from the phase-1 agent.
func TestHarnessDoesNotSeedDecisions(t *testing.T) {
	anchor := testAnchorBin(t)
	project := t.TempDir()
	st := &runState{cfg: &Config{Timeout: time.Minute, AnchorBin: anchor}, anchorBin: anchor}
	if err := st.setupAnchor(context.Background(), project); err != nil {
		t.Fatalf("setupAnchor: %v", err)
	}
	res := st.anchor(context.Background(), project, "tree", "--dir", project, "--json")
	if res.ExitCode != 0 {
		t.Fatalf("anchor tree failed: %s", res.Output)
	}
	var tree struct {
		Nodes []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(res.Output), &tree); err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, n := range tree.Nodes {
		types[n.Type]++
		if n.Type == "decision" {
			t.Fatalf("the harness seeded a decision node %q; decisions must come from the phase-1 agent", n.ID)
		}
	}
	if types["goal"] != 1 || types["task"] != 2 {
		t.Fatalf("unexpected harness-seeded tree: %v", types)
	}
}

// TestVariantRandomization checks the seeded shuffled rotation: deterministic
// under a seed, covers every variant, and changes with the seed.
func TestVariantRandomization(t *testing.T) {
	fixtures, err := loadFixtures(fixturesRoot(t), []string{"money"})
	if err != nil {
		t.Fatal(err)
	}
	fx := fixtures[0]
	order := func(seed int64, trials int) []string {
		rng := rand.New(rand.NewSource(seed))
		vs := assignVariants(rng, fx, trials)
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.ID
		}
		return out
	}
	a := order(42, 6)
	b := order(42, 6)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("variant assignment is not deterministic under a fixed seed: %v vs %v", a, b)
	}
	seen := map[string]int{}
	for _, id := range a {
		seen[id]++
	}
	for _, v := range fx.Variants {
		if seen[v.ID] == 0 {
			t.Fatalf("variant %s never used in 6 trials: %v", v.ID, a)
		}
	}
	differs := false
	for seed := int64(1); seed <= 10; seed++ {
		if strings.Join(order(seed, 6), ",") != strings.Join(a, ",") {
			differs = true
			break
		}
	}
	if !differs {
		t.Fatalf("variant assignment does not change with the seed")
	}
}

// TestFixturesDoNotLeakConventions statically checks the design invariant:
// visible tests and repository sources never mention the hidden conventions, and
// the phase-2 prompt is generic (no variant text).
func TestFixturesDoNotLeakConventions(t *testing.T) {
	fixtures, err := loadFixtures(fixturesRoot(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &runState{cfg: &Config{}}
	for _, fx := range fixtures {
		if len(fx.Variants) < 3 {
			t.Fatalf("%s has %d variants, want >= 3", fx.Name, len(fx.Variants))
		}
		repoText := readTreeText(t, fx.RepoDir)
		for _, v := range fx.Variants {
			for _, visible := range fx.CanonicalTests {
				if strings.Contains(string(visible), v.Phrase) || strings.Contains(string(visible), " "+v.ID) {
					t.Fatalf("%s visible tests leak variant %q", fx.Name, v.ID)
				}
			}
			if strings.Contains(repoText, v.ID) || strings.Contains(repoText, v.Phrase) {
				t.Fatalf("%s repository sources leak variant %q", fx.Name, v.ID)
			}
			if strings.Contains(fx.Phase2Prompt, v.ID) || strings.Contains(fx.Phase2Prompt, v.Phrase) {
				t.Fatalf("%s phase-2 prompt leaks variant %q", fx.Name, v.ID)
			}
			// Both arms' phase-1 prompts must state the convention explicitly.
			for _, arm := range []string{armBaseline, armFull} {
				prompt := promptPhase1(fx, &v, arm)
				if !strings.Contains(prompt, v.Statement) {
					t.Fatalf("%s phase-1 prompt for arm %s does not state the convention", fx.Name, arm)
				}
			}
			// Baseline phase 2 must not carry the convention.
			baselinePrompt := buildPhase2Prompt(state, fx, &v, armBaseline, "", "", "", "")
			if strings.Contains(baselinePrompt, v.ID) || strings.Contains(baselinePrompt, v.Phrase) {
				t.Fatalf("%s baseline phase-2 prompt leaks variant %q", fx.Name, v.ID)
			}
			// A packet must convey it.
			packetPrompt := buildPhase2Prompt(state, fx, &v, armPacket, "", "", "p.json", "{\"decision\":\""+v.Statement+"\"}")
			if !variantInText(&v, packetPrompt) {
				t.Fatalf("%s packet arm does not convey variant %q", fx.Name, v.ID)
			}
		}
	}
}

func loadFixtureForTest(t *testing.T, task string) *Fixture {
	t.Helper()
	fixtures, err := loadFixtures(fixturesRoot(t), []string{task})
	if err != nil {
		t.Fatal(err)
	}
	return fixtures[0]
}

func readTreeText(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestStats checks the statistics helpers used in the report.
func TestStats(t *testing.T) {
	if got := passK(3, 3, 3); math.Abs(got-1) > 1e-12 {
		t.Fatalf("passK(3,3,3) = %v, want 1", got)
	}
	if got := passK(2, 3, 3); got != 0 {
		t.Fatalf("passK(2,3,3) = %v, want 0", got)
	}
	if got := passK(2, 3, 1); math.Abs(got-2.0/3.0) > 1e-12 {
		t.Fatalf("passK(2,3,1) = %v, want 2/3", got)
	}
	ci := wilson(0, 10)
	if ci.Lower != 0 || ci.Upper <= 0 || ci.Upper >= 1 {
		t.Fatalf("wilson(0,10) = %+v", ci)
	}
	ci = wilson(10, 10)
	if ci.Lower <= 0 || ci.Lower >= 1 || ci.Upper < ci.Lower {
		t.Fatalf("wilson(10,10) = %+v", ci)
	}
	if got := wilson(0, 0); got != (WilsonCI{}) {
		t.Fatalf("wilson(0,0) = %+v, want zero", got)
	}
	if got := roundUSD(0.1 + 0.2); got != 0.3 {
		t.Fatalf("roundUSD(0.1+0.2) = %v, want 0.3", got)
	}
}

// TestSplitCommandAndAgentArgv checks the shell-free agent command parsing and
// the prompt/argv contract used for real agents.
func TestSplitCommandAndAgentArgv(t *testing.T) {
	fields, err := splitCommand(`claude -p --model "deepseek v4" --tag 'a b'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "--model", "deepseek v4", "--tag", "a b"}
	if strings.Join(fields, "|") != strings.Join(want, "|") {
		t.Fatalf("splitCommand = %v, want %v", fields, want)
	}
	if _, err := splitCommand(`unterminated "quote`); err == nil {
		t.Fatal("splitCommand accepted an unterminated quote")
	}
	argv, err := agentArgv(&Config{AgentCmd: "claude -p"}, "PROMPT TEXT", "/tmp/p.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, "|") != "claude|-p|PROMPT TEXT" {
		t.Fatalf("agentArgv appended prompt incorrectly: %v", argv)
	}
	argv, err = agentArgv(&Config{AgentCmd: "agent --prompt-file {prompt_file}"}, "PROMPT", "/tmp/p.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, "|") != "agent|--prompt-file|/tmp/p.md" {
		t.Fatalf("agentArgv {prompt_file} substitution failed: %v", argv)
	}
	if argv, _ = agentArgv(&Config{AgentCmd: "agent {prompt}"}, "PROMPT", "/tmp/p.md"); argv[len(argv)-1] != "PROMPT" {
		t.Fatalf("agentArgv {prompt} substitution failed: %v", argv)
	}
}

// TestRealAgentPlumbing exercises the non-mock agent path end to end with a
// harmless real process (/bin/echo). It cannot pass the task, which is fine:
// the point is that the prompt is passed, the process runs in the project and
// the report records the failure instead of crashing.
func TestRealAgentPlumbing(t *testing.T) {
	if _, err := os.Stat("/bin/echo"); err != nil {
		t.Skip("/bin/echo is not available")
	}
	cfg := &Config{
		Trials:      1,
		Arms:        []string{armBaseline},
		MockAgent:   false,
		AgentCmd:    "/bin/echo",
		Tasks:       []string{"money"},
		Timeout:     2 * time.Minute,
		OutDir:      t.TempDir(),
		FixturesDir: fixturesRoot(t),
		GoBin:       "go",
	}
	report := runForTest(t, cfg)
	if report.Config.MockAgent {
		t.Fatal("report claims mock mode for a real-agent run")
	}
	if len(report.Trials) != 1 {
		t.Fatalf("got %d trials, want 1", len(report.Trials))
	}
	row := report.Trials[0]
	if row.AgentExitPhase1 != 0 {
		t.Fatalf("echo agent exit = %d, want 0", row.AgentExitPhase1)
	}
	if row.Phase1Pass {
		t.Fatal("an echo agent cannot pass the phase-1 acceptance tests")
	}
	logPath := filepath.Join(report.Artifacts.Root, row.Artifacts["log_phase1_agent"])
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read phase-1 agent log: %v", err)
	}
	if !strings.Contains(string(log), "Phase 1") {
		t.Fatalf("prompt text was not passed to the real agent: %s", string(log))
	}
}

// TestBudgetTruncation checks the --max-tokens/--max-usd caps.
func TestBudgetTruncation(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Arms = []string{armBaseline}
	cfg.Tasks = []string{"money"}
	cfg.Trials = 3
	cfg.MaxTokens = 1 // one mock trial reports 2800 tokens
	report := runForTest(t, cfg)
	if !report.Truncated || !report.BudgetExceeded {
		t.Fatalf("budget truncation not reported: truncated=%v exceeded=%v", report.Truncated, report.BudgetExceeded)
	}
	if len(report.Trials) != 1 {
		t.Fatalf("ran %d trials under a 1-token cap, want 1", len(report.Trials))
	}
	if report.Budget.TrialsRun != 1 || report.Budget.TrialsPlanned != 3 {
		t.Fatalf("budget counts wrong: %+v", report.Budget)
	}
	if report.Budget.TokensUsed <= 0 || report.BudgetNote == "" {
		t.Fatalf("budget note/tokens missing: %+v", report.Budget)
	}
	md, err := os.ReadFile(report.Artifacts.ReportMD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "truncated") {
		t.Fatalf("report.md does not mention truncation")
	}
	var decoded Report
	b, err := os.ReadFile(report.Artifacts.ReportJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("report.json does not round-trip: %v", err)
	}
	if decoded.Budget.MaxTokens != 1 || decoded.SchemaVersion != schemaVersion {
		t.Fatalf("report.json lost budget/schema fields: %+v", decoded.Budget)
	}
}

// TestCLIJSON drives the real CLI entry point with --json and checks the
// printed report and exit code.
func TestCLIJSON(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr strings.Builder
	rc := runCLI([]string{
		"--mock-agent", "--tasks", "money", "--trials", "1",
		"--arms", "baseline,packet", "--out", out, "--json",
	}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("runCLI exit=%d stderr=%s", rc, stderr.String())
	}
	var report Report
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("--json output is not a report: %v\n%s", err, stdout.String())
	}
	if report.Suite != suiteName || len(report.Trials) != 2 {
		t.Fatalf("unexpected JSON report: suite=%q trials=%d", report.Suite, len(report.Trials))
	}
	if _, err := os.Stat(filepath.Join(out, "report.json")); err != nil {
		t.Fatalf("report.json missing after CLI run: %v", err)
	}
}

// TestCLIValidation checks the argument validation paths.
func TestCLIValidation(t *testing.T) {
	cases := [][]string{
		{},                                   // no agent mode
		{"--mock-agent", "--agent-cmd", "x"}, // both modes
		{"--mock-agent", "--arms", "bogus"},  // bad arm
		{"--mock-agent", "--mock-mode", "evil"},
		{"--mock-agent", "--trials", "0"},
		{"--mock-agent", "--tasks", "nope", "--out", os.TempDir()},
	}
	for _, args := range cases {
		var stdout, stderr strings.Builder
		if rc := runCLI(args, &stdout, &stderr); rc == 0 {
			t.Fatalf("runCLI(%v) exited 0, want non-zero", args)
		}
	}
}

// TestReadmeMentionsMechanics is a cheap documentation guard: the README must
// explain the non-seeding design and the mock-mode limits.
func TestReadmeMentionsMechanics(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil || root == "" {
		t.Fatalf("module root: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "bench", "stress", "README.md"))
	if err != nil {
		t.Fatalf("README: %v", err)
	}
	text := string(b)
	for _, want := range []string{"non-seeding", "mock", "limitation", "ablation", "anchor wrap"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Fatalf("README does not mention %q", want)
		}
	}
}
