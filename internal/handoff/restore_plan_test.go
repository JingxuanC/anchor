package handoff

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/core"
)

func appendHandoffEventH(t *testing.T, dir string, typ core.EventType, p core.HandoffPayload) {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(core.Event{Type: typ, CausalParents: []string{}, Payload: b}); err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
}

func preparePlanFixture(t *testing.T) (dir, repo string, p Packet, path string) {
	t.Helper()
	repo = initRepo(t)
	dir = t.TempDir()
	initWorkspace(t, dir)
	gc := appendOwnership(t, dir, "g1", "lg")
	appendNode(t, dir, "g1", "goal", "Goal", "", "lg", core.NodePayload{NorthStar: "north"}, gc)
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	return dir, repo, p, path
}

func TestBuildPlanFreshMatchesAndIsAckable(t *testing.T) {
	dir, repo, p, path := preparePlanFixture(t)
	plan, err := BuildPlan(dir, repo, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecoveryStatus != StatusComplete || plan.HandoffID != p.HandoffID {
		t.Fatalf("plan status wrong: %+v", plan)
	}
	if !plan.Code.HeadMatches || !plan.Code.BranchMatches || !plan.Code.StagedDiffMatches {
		t.Fatalf("code comparison wrong: %+v", plan.Code)
	}
	if !plan.CanAck || plan.CanVerify {
		t.Fatalf("fresh plan should be ackable but not verifiable: %+v", plan)
	}
	if !strings.Contains(plan.CodeApply, "disabled") {
		t.Fatalf("plan must state code apply is disabled: %q", plan.CodeApply)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != "ack" {
		t.Fatalf("expected a single ack action, got %+v", plan.Actions)
	}
	// Progress is empty before any event.
	if plan.Progress.Acked || plan.Progress.Verified {
		t.Fatalf("unexpected progress: %+v", plan.Progress)
	}
}

func TestBuildPlanStaleCodeAndGateLifecycle(t *testing.T) {
	dir, repo, p, path := preparePlanFixture(t)
	// Move the repository forward: the packet is now stale.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "add", "tracked.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if err := exec.Command("git", "-C", repo, "-c", "user.email=t@example.invalid", "-c", "user.name=T", "commit", "-qm", "advance").Run(); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(dir, repo, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecoveryStatus != StatusStaleCode {
		t.Fatalf("expected stale_code, got %s (%v)", plan.RecoveryStatus, plan.Reasons)
	}
	if plan.Code.HeadMatches {
		t.Fatalf("head should differ: %+v", plan.Code)
	}
	if plan.CanAck || plan.CanVerify {
		t.Fatalf("stale packet should block ack/verify without force: %+v", plan)
	}
	if !hasAction(plan.Actions, "checkout") {
		t.Fatalf("plan should suggest a checkout action: %+v", plan.Actions)
	}
	if len(plan.AckBlockers) == 0 || !strings.Contains(strings.Join(plan.AckBlockers, " "), "force") {
		t.Fatalf("ack blocker should mention force: %+v", plan.AckBlockers)
	}

	// The gate is event-based: record the preparation first.
	appendHandoffEventH(t, dir, core.EvHandoffPrepared, core.HandoffPayload{HandoffID: p.HandoffID, ContentHash: p.ContentHash, CodeHead: p.CodeState.Head})
	gate, err := Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Pending || gate.Progress.Verified || gate.HandoffID != p.HandoffID {
		t.Fatalf("gate should be pending before ack: %+v", gate)
	}

	appendHandoffEventH(t, dir, core.EvHandoffAcked, core.HandoffPayload{HandoffID: p.HandoffID, ContentHash: p.ContentHash, Forced: true})
	gate, err = Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Pending || !gate.Progress.Acked || gate.Progress.Verified {
		t.Fatalf("gate should stay pending after ack: %+v", gate)
	}

	appendHandoffEventH(t, dir, core.EvHandoffVerified, core.HandoffPayload{HandoffID: p.HandoffID, CodeHead: plan.Code.ActualHead, Forced: true})
	gate, err = Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gate.Pending || !gate.Progress.Verified {
		t.Fatalf("gate should clear after verification: %+v", gate)
	}

	plan, err = BuildPlan(dir, repo, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Progress.Acked || !plan.Progress.Verified || plan.CanAck {
		t.Fatalf("plan progress wrong after ack+verify: %+v", plan.Progress)
	}
	if !hasAction(plan.Actions, "resume") {
		t.Fatalf("verified plan should offer resume: %+v", plan.Actions)
	}
}

func TestBuildPlanVerifyRequiresAckAndMatchingHead(t *testing.T) {
	dir, repo, p, path := preparePlanFixture(t)
	plan, err := BuildPlan(dir, repo, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanVerify || len(plan.VerifyBlockers) == 0 {
		t.Fatalf("verify should be blocked before ack: %+v", plan)
	}
	appendHandoffEventH(t, dir, core.EvHandoffAcked, core.HandoffPayload{HandoffID: p.HandoffID})
	// Corrupt the packet's head so verification is blocked on a head mismatch.
	p.CodeState.Head = strings.Repeat("c", 40)
	plan, err = BuildPlan(dir, repo, p, path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanVerify {
		t.Fatalf("verify should be blocked by a head mismatch: %+v", plan)
	}
	if !strings.Contains(strings.Join(plan.VerifyBlockers, " "), "force") {
		t.Fatalf("blocker should mention --force: %+v", plan.VerifyBlockers)
	}
}

func TestGateWithoutPacketIsNotPending(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir)
	gate, err := Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gate.Pending || gate.HandoffID != "" {
		t.Fatalf("empty workspace should have no handoff gate: %+v", gate)
	}
}

func TestBuildPlanReportsCodeStateReadFailure(t *testing.T) {
	dir, _, p, path := preparePlanFixture(t)
	plan, err := BuildPlan(dir, filepath.Join(t.TempDir(), "norepo"), p, path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RecoveryStatus != StatusIncomplete {
		t.Fatalf("expected incomplete when the repo cannot be read, got %s", plan.RecoveryStatus)
	}
	if !strings.Contains(strings.Join(plan.Reasons, " "), "cannot read code state") {
		t.Fatalf("plan should report the read failure: %v", plan.Reasons)
	}
}

func hasAction(actions []RestoreAction, kind string) bool {
	for _, a := range actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// TestGateSurvivesPacketDeletion covers the review finding that deleting the
// packet file used to disable the successor gate.
func TestGateSurvivesPacketDeletion(t *testing.T) {
	dir, _, p, path := preparePlanFixture(t)
	if err := RecordPrepared(dir, p); err != nil {
		t.Fatal(err)
	}
	gate, err := Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Pending || gate.PacketMissing || gate.HandoffID != p.HandoffID {
		t.Fatalf("gate should be pending with its packet present: %+v", gate)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	gate, err = Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Pending || !gate.PacketMissing || gate.HandoffID != p.HandoffID {
		t.Fatalf("deleting the packet must not disable the gate: %+v", gate)
	}
	// A later preparation supersedes the old id.
	p2, path2, err := Prepare(dir, "")
	if err == nil {
		_ = path2
		if err := RecordPrepared(dir, p2); err != nil {
			t.Fatal(err)
		}
		gate, err = Gate(dir)
		if err != nil {
			t.Fatal(err)
		}
		if gate.HandoffID != p2.HandoffID || !gate.Pending {
			t.Fatalf("new preparation should become the gated handoff: %+v", gate)
		}
	}
}

func TestRecordPreparedIsIdempotencySafe(t *testing.T) {
	dir, _, p, _ := preparePlanFixture(t)
	if err := RecordPrepared(dir, p); err != nil {
		t.Fatal(err)
	}
	// Preparing the same packet twice is an event-level conflict, surfaced by the
	// store; a fresh preparation gets a new id instead.
	again, _, err := Prepare(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.HandoffID == p.HandoffID {
		t.Fatal("a new preparation reused the previous handoff id")
	}
	if err := RecordPrepared(dir, again); err != nil {
		t.Fatal(err)
	}
	gate, err := Gate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gate.HandoffID != again.HandoffID || !gate.Pending {
		t.Fatalf("latest preparation should gate the workspace: %+v", gate)
	}
}
