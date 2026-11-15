package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMockHarnessReport runs the full harness once per arm with the
// deterministic mock agent and checks the report contract:
//
//   - the run is offline and finishes quickly;
//   - report.json and report.md exist and report.json round-trips;
//   - the mock baseline passes phase 1 but fails the phase-2 resume;
//   - the mock anchor arm passes both phases, prepares a complete handoff and
//     earns the resume-decision bonus;
//   - the hypotheses are evaluated and rendered.
func TestMockHarnessReport(t *testing.T) {
	started := time.Now()
	outDir := t.TempDir()
	cfg := Config{
		Trials:    1,
		Arm:       armBoth,
		MockAgent: true,
		Timeout:   2 * time.Minute,
		OutDir:    outDir,
	}
	report, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("mock harness took %v, want < 10s", elapsed)
	}
	if report.Skipped {
		t.Fatalf("mock run was skipped: %s", report.SkipReason)
	}
	if report.SchemaVersion != schemaVersion {
		t.Fatalf("schema version = %d, want %d", report.SchemaVersion, schemaVersion)
	}

	for _, path := range []string{report.Artifacts.JSON, report.Artifacts.Markdown} {
		if info, err := os.Stat(path); err != nil || info.IsDir() || info.Size() == 0 {
			t.Fatalf("artifact %s missing or empty: %v", path, err)
		}
	}

	if len(report.Trials) != 2 {
		t.Fatalf("got %d trial rows, want 2 (one per arm)", len(report.Trials))
	}
	base, ok := report.Aggregates[armBaseline]
	if !ok {
		t.Fatalf("missing %s aggregate: %+v", armBaseline, report.Aggregates)
	}
	anchor, ok := report.Aggregates[armAnchor]
	if !ok {
		t.Fatalf("missing %s aggregate: %+v", armAnchor, report.Aggregates)
	}
	if base.Trials != 1 || anchor.Trials != 1 {
		t.Fatalf("trials per arm = %d/%d, want 1/1", base.Trials, anchor.Trials)
	}
	if base.Phase1PassRate != 1 {
		t.Fatalf("baseline phase-1 pass rate = %v, want 1", base.Phase1PassRate)
	}
	if base.Phase2PassRate != 0 {
		t.Fatalf("baseline phase-2 pass rate = %v, want 0 (mock simulates context loss)", base.Phase2PassRate)
	}
	if anchor.Phase1PassRate != 1 {
		t.Fatalf("anchor phase-1 pass rate = %v, want 1", anchor.Phase1PassRate)
	}
	if anchor.Phase2PassRate != 1 {
		t.Fatalf("anchor phase-2 pass rate = %v, want 1", anchor.Phase2PassRate)
	}
	if anchor.HandoffPreparedRate != 1 || anchor.HandoffCompleteRate != 1 {
		t.Fatalf("anchor handoff prepared/complete = %v/%v, want 1/1",
			anchor.HandoffPreparedRate, anchor.HandoffCompleteRate)
	}
	if base.MeanResumeScore != 0 {
		t.Fatalf("baseline mean resume score = %v, want 0", base.MeanResumeScore)
	}
	if anchor.MeanResumeScore != 2 {
		t.Fatalf("anchor mean resume score = %v, want 2 (pass + decision bonus)", anchor.MeanResumeScore)
	}

	for _, id := range []string{"H1", "H2", "H3"} {
		hyp, ok := report.Hypotheses[id]
		if !ok {
			t.Fatalf("missing hypothesis %s", id)
		}
		if hyp.Status != "supported" {
			t.Fatalf("%s status = %q (%s), want supported for the deterministic mock", id, hyp.Status, hyp.Detail)
		}
	}
	if report.Delta == nil || report.Delta.Phase2PassRate != 1 {
		t.Fatalf("delta = %+v, want phase-2 pass delta 1", report.Delta)
	}

	var anchorRow, baselineRow *TrialResult
	for i := range report.Trials {
		switch report.Trials[i].Arm {
		case armBaseline:
			baselineRow = &report.Trials[i]
		case armAnchor:
			anchorRow = &report.Trials[i]
		}
	}
	if baselineRow == nil || anchorRow == nil {
		t.Fatalf("missing trial rows: %+v", report.Trials)
	}
	if baselineRow.AnchorEvents != 0 || baselineRow.HandoffPrepared {
		t.Fatalf("baseline must not have Anchor state: events=%d handoff=%v",
			baselineRow.AnchorEvents, baselineRow.HandoffPrepared)
	}
	if anchorRow.AnchorEvents == 0 {
		t.Fatal("anchor trial recorded no Anchor events")
	}
	if !anchorRow.DecisionMarker || anchorRow.ResumeScore != 2 {
		t.Fatalf("anchor decision marker/resume score = %v/%d, want true/2",
			anchorRow.DecisionMarker, anchorRow.ResumeScore)
	}
	if anchorRow.HandoffID == "" {
		t.Fatal("anchor trial has no handoff id")
	}
	if anchorRow.WrapFallback != "" {
		t.Logf("anchor trial fell back to a direct run: %s", anchorRow.WrapFallback)
	}

	md, err := os.ReadFile(report.Artifacts.Markdown)
	if err != nil {
		t.Fatalf("read report.md: %v", err)
	}
	for _, want := range []string{
		"# Anchor A/B benchmark report",
		"## Results",
		"## Hypotheses",
		"## Environment and honest caveats",
		"deterministic mock agent",
		"baseline",
		"anchor",
	} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("report.md is missing %q", want)
		}
	}

	raw, err := os.ReadFile(report.Artifacts.JSON)
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("report.json is not valid JSON: %v", err)
	}
	if len(decoded.Trials) != 2 || len(decoded.Aggregates) != 2 {
		t.Fatalf("decoded report shape = %d trials, %d aggregates; want 2/2",
			len(decoded.Trials), len(decoded.Aggregates))
	}
}
