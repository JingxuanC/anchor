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

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Anchor Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-qm", "initial")
	return repo
}

func initWorkspace(t *testing.T, dir string) {
	t.Helper()
	if _, err := core.InitWorkspace(dir, "ws-test", "dev-test"); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareCleanRepoIsComplete(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusComplete {
		t.Fatalf("want complete, got %s warnings=%v", p.RecoveryStatus, p.Warnings)
	}
	if p.ArtifactAvailability != "metadata_only" {
		t.Fatalf("unexpected availability %s", p.ArtifactAvailability)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusComplete {
		t.Fatalf("want complete eval, got %+v", ev)
	}
}

func TestDirtyRepoIsIncompleteAndSecretNotInPacket(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "ANCHOR_TEST_SECRET_SHOULD_NOT_APPEAR"
	if err := os.WriteFile(filepath.Join(repo, "untracked.env"), []byte("TOKEN="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	initWorkspace(t, dir)
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusIncomplete {
		t.Fatalf("want incomplete, got %s", p.RecoveryStatus)
	}
	if p.ArtifactAvailability != "metadata_only" {
		t.Fatalf("want metadata_only, got %s", p.ArtifactAvailability)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret content leaked into handoff packet")
	}
	if strings.Contains(string(raw), repo) {
		t.Fatal("absolute repo path leaked into handoff packet")
	}
	var decoded Packet
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range decoded.CodeState.Untracked {
		if f.Path == "untracked.env" && f.SHA256 != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("untracked manifest missing hash")
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusIncomplete {
		t.Fatalf("dirty handoff must evaluate incomplete, got %+v", ev)
	}
}

func TestEvaluateDetectsStaleCodeWithoutOverwriting(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	// Create a new commit after prepare.
	cmd := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-qm", "drift")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusStaleCode {
		t.Fatalf("want stale_code, got %+v", ev)
	}
}

func TestNoPacketIsIncompleteAndPathsAreSafe(t *testing.T) {
	dir := t.TempDir()
	initWorkspace(t, dir)
	ev := Evaluate(dir, t.TempDir(), nil)
	if ev.Status != StatusIncomplete {
		t.Fatalf("want incomplete, got %+v", ev)
	}
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../b", "C:/secret", "a:b"} {
		if err := CheckPath(p); err == nil {
			t.Fatalf("unsafe path %q accepted", p)
		}
	}
	if err := CheckPath("src/main.go"); err != nil {
		t.Fatalf("safe path rejected: %v", err)
	}
}

func TestContentHashMismatchRejected(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), `"summary": "`, `"summary": "TAMPERED `, 1))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("tampered packet accepted")
	}
	_ = p
}

func TestQuarantinedEventForcesIncompleteHandoff(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	// A corrupt event file must not be treated as a complete workspace.
	if err := os.WriteFile(filepath.Join(dir, "events", "bad.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusIncomplete || len(p.Quarantined) != 1 {
		t.Fatalf("quarantine not reflected: %+v", p)
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusIncomplete {
		t.Fatalf("quarantined evaluation must be incomplete: %+v", ev)
	}
}

func appendOwnership(t *testing.T, dir, node, lease string, parents ...string) string {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := core.OwnershipPayload{NodeID: node, Owner: "agent", LeaseID: lease, FencingToken: 1}
	b, _ := json.Marshal(p)
	e, _, err := store.Append(core.Event{Type: core.EvOwnershipClaimed, CausalParents: parents, LeaseID: lease, FencingToken: 1, Payload: b})
	if err != nil {
		t.Fatal(err)
	}
	return e.EventID
}

func appendNode(t *testing.T, dir, node, typ, title, parent, lease string, payload core.NodePayload, parents ...string) string {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	payload.NodeID = node
	payload.Type = typ
	payload.Title = title
	payload.ParentID = parent
	payload.Owner = "agent"
	b, _ := json.Marshal(payload)
	e, _, err := store.Append(core.Event{Type: core.EvNodeCreated, CausalParents: parents, LeaseID: lease, FencingToken: 1, Payload: b})
	if err != nil {
		t.Fatal(err)
	}
	return e.EventID
}

// TestPrepareCarriesTaskStructure covers REQ-RST-001/010 and REQ-ST-001/002/005:
// the packet must carry the constraints, decisions, next steps, open questions,
// ownership and provenance a successor needs.
func TestPrepareCarriesTaskStructure(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)

	gc := appendOwnership(t, dir, "g1", "lg")
	appendNode(t, dir, "g1", "goal", "Ship local segment", "", "lg", core.NodePayload{
		NorthStar:       "successor resumes with zero context loss",
		Must:            []string{"keep evidence immutable"},
		MustNot:         []string{"no local causal database"},
		SuccessCriteria: []string{"restore works on a second device"},
		Provenance:      string(core.ProvenanceAgentInferred),
	}, gc)
	dc := appendOwnership(t, dir, "d1", "ld", gc)
	appendNode(t, dir, "d1", "decision", "Hash the packet", "g1", "ld", core.NodePayload{
		Decision:     "hash the packet body",
		Rationale:    "detect tampering",
		Alternatives: []string{"sign with a key"},
		Consequences: []string{"no key management"},
		Provenance:   string(core.ProvenanceImported),
	}, dc)
	tc := appendOwnership(t, dir, "t1", "lt", gc)
	appendNode(t, dir, "t1", "task", "Carry the event log", "g1", "lt", core.NodePayload{}, tc)
	qc := appendOwnership(t, dir, "q1", "lq", gc)
	appendNode(t, dir, "q1", "question", "Which sync topology?", "g1", "lq", core.NodePayload{}, qc)

	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.Goal == nil {
		t.Fatal("packet has no goal block")
	}
	if p.Goal.ID != "g1" || p.Goal.NorthStar != "successor resumes with zero context loss" {
		t.Fatalf("goal block lost the north star: %+v", p.Goal)
	}
	if len(p.Goal.Must) != 1 || len(p.Goal.MustNot) != 1 || len(p.Goal.SuccessCriteria) != 1 {
		t.Fatalf("goal constraints missing: %+v", p.Goal)
	}
	if len(p.Decisions) != 1 || p.Decisions[0].Rationale != "detect tampering" || len(p.Decisions[0].Alternatives) != 1 {
		t.Fatalf("decision block incomplete: %+v", p.Decisions)
	}
	if p.Decisions[0].Provenance != string(core.ProvenanceImported) {
		t.Fatalf("decision provenance lost: %+v", p.Decisions[0])
	}
	if len(p.NextSteps) != 1 || p.NextSteps[0].ID != "t1" {
		t.Fatalf("next steps missing: %+v", p.NextSteps)
	}
	if len(p.OpenQuestions) != 1 || p.OpenQuestions[0].ID != "q1" {
		t.Fatalf("open questions missing: %+v", p.OpenQuestions)
	}
	if len(p.Ownership) != 4 {
		t.Fatalf("ownership block incomplete: %+v", p.Ownership)
	}
	wantProv := map[string]int{
		string(core.ProvenanceAgentInferred): 1, // goal
		string(core.ProvenanceImported):      1, // decision
		"unspecified":                        2, // task + question created without provenance
	}
	for k, want := range wantProv {
		if p.ProvenanceSummary[k] != want {
			t.Fatalf("provenance summary[%q] = %d, want %d (%+v)", k, p.ProvenanceSummary[k], want, p.ProvenanceSummary)
		}
	}
	// The packet hash must cover the new fields.
	loaded, err := Load(mustPacketPath(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal == nil || loaded.Goal.NorthStar != p.Goal.NorthStar {
		t.Fatalf("packet round-trip lost the goal block: %+v", loaded.Goal)
	}
}

func mustPacketPath(t *testing.T, dir string) string {
	t.Helper()
	p, path, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	_ = p
	return path
}
