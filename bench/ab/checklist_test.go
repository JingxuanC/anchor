package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func runMock(t *testing.T, mode string, trials int) *Report {
	t.Helper()
	cfg := Config{Trials: trials, Arm: armBoth, MockAgent: true, MockMode: mode,
		Timeout: 2 * time.Minute, OutDir: t.TempDir()}
	report, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run (%s): %v", mode, err)
	}
	return report
}

func TestMockNormalIsClean(t *testing.T) {
	report := runMock(t, "normal", 1)
	for _, row := range report.Trials {
		if row.TestsModified {
			t.Fatalf("normal mock flagged tests_modified: %+v", row.CheatFlags)
		}
		if len(row.CheatFlags) != 0 {
			t.Fatalf("normal mock produced cheat flags: %v", row.CheatFlags)
		}
	}
	anchor := report.Aggregates[armAnchor]
	if anchor.Phase1PassK != 1 || anchor.Phase2PassK != 1 {
		t.Fatalf("normal mock anchor pass^k = %v/%v", anchor.Phase1PassK, anchor.Phase2PassK)
	}
	if anchor.Phase1CI95Low < 0 || anchor.Phase1CI95High > 1 {
		t.Fatalf("CI out of range: [%v,%v]", anchor.Phase1CI95Low, anchor.Phase1CI95High)
	}
	// Checklist artifacts: patches are captured for each phase.
	for _, row := range report.Trials {
		for _, name := range []string{"patch-phase1.diff", "patch-phase2.diff"} {
			if _, err := os.Stat(filepath.Join(row.LogDir, name)); err != nil {
				t.Fatalf("%s missing for trial %d %s: %v", name, row.Trial, row.Arm, err)
			}
		}
	}
}

func TestMockSabotageFailsEverything(t *testing.T) {
	report := runMock(t, "sabotage", 1)
	for _, row := range report.Trials {
		if row.Phase1Pass || row.Phase2Pass {
			t.Fatalf("sabotage mock passed a phase: %+v", row)
		}
	}
	for _, arm := range []string{armBaseline, armAnchor} {
		if report.Aggregates[arm].Phase1PassRate != 0 {
			t.Fatalf("%s sabotage pass rate = %v", arm, report.Aggregates[arm].Phase1PassRate)
		}
	}
}

func TestMockCheaterIsDetectedAndCannotPass(t *testing.T) {
	report := runMock(t, "cheater", 1)
	flagged := false
	for _, row := range report.Trials {
		if row.TestsModified {
			flagged = true
			joined := ""
			for _, f := range row.CheatFlags {
				joined += f + "|"
			}
			if !contains(joined, "modified visible test file") {
				t.Fatalf("cheater flag text missing: %v", row.CheatFlags)
			}
		}
		if row.Phase1Pass || row.Phase2Pass {
			t.Fatalf("cheater mock passed a phase (canonical tests were not restored): %+v", row)
		}
	}
	if !flagged {
		t.Fatal("cheater mock was not flagged as tests_modified")
	}
	for _, arm := range []string{armBaseline, armAnchor} {
		if report.Aggregates[arm].TestsModifiedCount == 0 {
			t.Fatalf("%s aggregate did not count the modified tests", arm)
		}
	}
}

func TestReportMetadataAndTaskLock(t *testing.T) {
	report := runMock(t, "normal", 1)
	if report.Config.Suite != "resume/ratelimit" {
		t.Fatalf("suite = %q", report.Config.Suite)
	}
	if len(report.Config.TaskHash) != 64 {
		t.Fatalf("task hash not a sha256: %q", report.Config.TaskHash)
	}
	if report.Config.MockMode != "normal" {
		t.Fatalf("mock mode not recorded: %q", report.Config.MockMode)
	}
	for _, key := range []string{"suite", "task_hash", "bench_commit", "anchor_version", "mock_mode"} {
		if report.Metadata[key] == "" {
			t.Fatalf("metadata missing %q: %v", key, report.Metadata)
		}
	}
	if report.Delta == nil {
		t.Fatal("delta missing")
	}
	if report.Delta.McNemarP < 0 || report.Delta.McNemarP > 1 {
		t.Fatalf("McNemar p out of range: %v", report.Delta.McNemarP)
	}
	if report.Delta.Phase1DeltaCI95Low > report.Delta.Phase1DeltaCI95High {
		t.Fatalf("paired CI inverted: %v..%v", report.Delta.Phase1DeltaCI95Low, report.Delta.Phase1DeltaCI95High)
	}
	// The task hash must be stable across runs.
	hash1, err := fixturesSHA256(report.Config.FixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := fixturesSHA256(report.Config.FixturesDir)
	if err != nil || hash1 != hash2 {
		t.Fatalf("fixture hash is not stable: %q vs %q (%v)", hash1, hash2, err)
	}
}

func TestBudgetCaps(t *testing.T) {
	trials := []TrialResult{
		{Phase1Tokens: TokenUsage{Parsed: true, Input: 600, Output: 100}},
		{Phase2Tokens: TokenUsage{Parsed: true, Input: 300, Output: 100, CostUSD: 0.5}},
	}
	if ok, note := budgetExceeded(trials, 0, 500); !ok || note == "" {
		t.Fatalf("token cap not enforced: %v %q", ok, note)
	}
	if ok, _ := budgetExceeded(trials, 0.25, 0); !ok {
		t.Fatalf("cost cap not enforced")
	}
	if ok, _ := budgetExceeded(trials, 10, 100000); ok {
		t.Fatal("budget exceeded under generous caps")
	}
	if ok, _ := budgetExceeded(trials, 0, 0); ok {
		t.Fatal("zero caps should mean unlimited")
	}
}

func TestCheatSuspicionFlags(t *testing.T) {
	flags := cheatSuspicionFlags("let me look at huggingface for the swe-bench task")
	if len(flags) < 2 {
		t.Fatalf("suspicion flags = %v", flags)
	}
	if flags := cheatSuspicionFlags("normal agent output"); len(flags) != 0 {
		t.Fatalf("false suspicion: %v", flags)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
