package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rawOwn(node, lease, owner string, token uint64, parents ...string) Event {
	p, _ := json.Marshal(OwnershipPayload{NodeID: node, Owner: owner, LeaseID: lease, FencingToken: token})
	return Event{Type: EvOwnershipClaimed, CausalParents: parents, LeaseID: lease, FencingToken: token, Payload: p}
}
func rawCreated(node, typ, parent, lease string, token uint64, parents ...string) Event {
	p, _ := json.Marshal(NodePayload{NodeID: node, Type: typ, Title: node, ParentID: parent})
	return Event{Type: EvNodeCreated, CausalParents: parents, LeaseID: lease, FencingToken: token, Payload: p}
}
func rawStatus(node string, to Status, base int, lease string, token uint64, parents ...string) Event {
	p, _ := json.Marshal(NodePayload{NodeID: node, Status: string(to)})
	return Event{Type: EvStatusChanged, CausalParents: parents, BaseRevision: base, ExpectedRevision: base + 1, LeaseID: lease, FencingToken: token, Payload: p}
}
func withID(e Event, id, device string, seq uint64) Event {
	e.EventID = id
	e.DeviceID = device
	e.DeviceSeq = seq
	return e
}

// P0-1: event_id must not escape events/ or the workspace.
func TestP0EventIDTraversalRejected(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	e := rawOwn("n1", "l1", "a", 1)
	e.EventID = "../../escaped"
	if _, _, err := s.Append(e); err == nil {
		t.Fatal("path-traversal event_id was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.json")); err == nil {
		t.Fatal("escaped file was written outside workspace")
	}
}

// P0-2: causal cycles must be isolated, never recurse to stack overflow.
func TestP0CausalCycleIsolated(t *testing.T) {
	events := []Event{
		withID(rawOwn("n1", "l1", "a", 1), "claim", "d1", 1),
		withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "created", "d1", 2),
		withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "b"), "a", "d1", 3),
		withID(rawStatus("n1", StatusBlocked, 1, "l1", 1, "a"), "b", "d1", 4),
	}
	r := Replay(events)
	found := false
	for _, c := range r.Conflicts {
		if c.Reason == "causal cycle isolated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cycle not isolated: %+v", r)
	}
}

// P0-3: concurrent takeovers are conflicts, not last-writer-wins.
func TestP0ConcurrentTakeoversConflict(t *testing.T) {
	goalClaim := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	goalCreated := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	claim := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "created", "d1", 4)
	ta := withID(rawOwn("n1", "la", "alice", 2, "claim"), "takeover-a", "d1", 5)
	tb := withID(rawOwn("n1", "lb", "bob", 2, "claim"), "takeover-b", "d2", 1)
	r := Replay([]Event{goalClaim, goalCreated, claim, created, ta, tb})
	seen := map[string]bool{}
	for _, c := range r.Conflicts {
		seen[c.EventID] = true
	}
	if !seen["takeover-a"] || !seen["takeover-b"] {
		t.Fatalf("concurrent takeovers not isolated: %+v", r.Conflicts)
	}
	if r.Tree.Nodes["n1"].Owner == "bob" || r.Tree.Nodes["n1"].Owner == "alice" {
		t.Fatalf("a concurrent takeover overwrote the owner: %+v", r.Tree.Nodes["n1"])
	}
}

// P0-4: latest ownership is causal, not the highest cross-device device_seq.
func TestP0LatestOwnershipUsesCausalityNotDeviceSeq(t *testing.T) {
	goalClaim := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	goalCreated := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	claim := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "created", "d1", 4)
	// A high device_seq ownership event on d1 is causally before the d2 takeover.
	old := withID(rawOwn("n1", "l-old", "old", 2, "claim"), "old-claim", "d1", 10)
	takeover := withID(rawOwn("n1", "l-new", "new", 3, "old-claim"), "takeover", "d2", 1)
	mut := withID(rawStatus("n1", StatusInProgress, 1, "l-new", 3, "created", "takeover"), "mut", "d2", 2)
	r := Replay([]Event{goalClaim, goalCreated, claim, created, old, takeover, mut})
	if r.Tree.Nodes["n1"].Status != StatusInProgress {
		t.Fatalf("mutation rejected due to device_seq ordering: %+v", r)
	}
	if len(r.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", r.Conflicts)
	}
}

func TestP1NonGoalRequiresParent(t *testing.T) {
	events := []Event{
		withID(rawOwn("n1", "l1", "a", 1), "claim", "d1", 1),
		withID(rawCreated("n1", "task", "", "l1", 1, "claim"), "created", "d1", 2),
	}
	r := Replay(events)
	if len(r.Conflicts) == 0 {
		t.Fatal("non-goal without parent must be rejected")
	}
}

func TestP1TombstoneIsTerminal(t *testing.T) {
	goalClaim := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	goalCreated := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	claim := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "created", "d1", 4)
	tomb := Event{EventID: "tomb", DeviceID: "d1", DeviceSeq: 5, Type: EvNodeTombstoned, CausalParents: []string{"created"}, BaseRevision: 1, ExpectedRevision: 2, LeaseID: "l1", FencingToken: 1, Payload: json.RawMessage(`{"node_id":"n1"}`)}
	after := withID(rawStatus("n1", StatusInProgress, 2, "l1", 1, "tomb"), "after", "d1", 6)
	r := Replay([]Event{goalClaim, goalCreated, claim, created, tomb, after})
	found := false
	for _, c := range r.Conflicts {
		if c.EventID == "after" && strings.Contains(c.Reason, "tombstoned") {
			found = true
		}
	}
	if !found {
		t.Fatalf("mutation after tombstone not rejected: %+v", r.Conflicts)
	}
}

func TestEventDirSymlinkRejectedAndDuplicateID(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "events")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err == nil {
		t.Fatal("symlinked events directory accepted")
	}
	ws2 := filepath.Join(root, "ws2")
	s, err := Open(ws2)
	if err != nil {
		t.Fatal(err)
	}
	e := rawOwn("n1", "l1", "a", 1)
	e.EventID = "dup"
	if _, _, err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Append(e); err == nil {
		t.Fatal("duplicate event_id accepted")
	}
}

func TestDoneRequiresEvidenceOrAttestedUserOverride(t *testing.T) {
	gc := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	ge := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	c := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	cr := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "created", "d1", 4)
	ip := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "created"), "a-inprog", "d1", 5)
	doneNoEvidence := withID(rawStatus("n1", StatusDone, 2, "l1", 1, "a-inprog"), "b-done", "d1", 6)
	r := Replay([]Event{gc, ge, c, cr, ip, doneNoEvidence})
	found := false
	for _, c := range r.Conflicts {
		if c.EventID == "b-done" {
			found = true
		}
	}
	if !found {
		t.Fatalf("done without evidence/override was accepted: %+v", r)
	}
	// Agent cannot self-report actor=user without local attestation.
	badOverride := doneNoEvidence
	badOverride.EventID = "b-done2"
	badOverride.ActorKind = "user"
	badOverride.ActorAttestation = ""
	badOverride.Payload = json.RawMessage(`{"node_id":"n1","status":"done","user_override":true}`)
	r = Replay([]Event{gc, ge, c, cr, ip, badOverride})
	found = false
	for _, c := range r.Conflicts {
		if c.EventID == "b-done2" {
			found = true
		}
	}
	if !found {
		t.Fatal("agent self-reported user override accepted")
	}
	// Structural replay only: production write/read paths use ReplayVerified to
	// re-hash persisted bytes. This assertion covers the state-machine wiring and
	// does not claim artifact authenticity.
	evPayload := json.RawMessage(`{"evidence_id":"ev1","node_id":"n1","kind":"test","sha256":"` + strings.Repeat("a", 64) + `"}`)
	ev := Event{EventID: "0evidence", DeviceID: "d1", DeviceSeq: 5, Type: EvEvidenceAdded, CausalParents: []string{"created"}, Payload: evPayload}
	okDone := doneNoEvidence
	okDone.EventID = "b-done3"
	okDone.DeviceSeq = 7
	okDone.CausalParents = []string{"a-inprog", "0evidence"}
	okDone.Payload = json.RawMessage(`{"node_id":"n1","status":"done","evidence_ids":["ev1"]}`)
	r = Replay([]Event{gc, ge, c, cr, ev, ip, okDone})
	if r.Tree.Nodes["n1"].Status != StatusDone {
		t.Fatalf("done with registered evidence rejected: %+v", r)
	}
}

func TestLoadReportQuarantinesBadHash(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	e := rawOwn("n1", "l1", "a", 1)
	e.EventID = "good"
	if _, _, err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "events", "bad.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 1 || len(r.Quarantined) != 1 {
		t.Fatalf("valid events not preserved: %+v", r)
	}
	if got := Replay(r.Events); got.RecoveryStatus != "complete" {
		t.Fatalf("valid events failed replay: %+v", got)
	}
}

func TestExpectedRevisionMismatchIsConflict(t *testing.T) {
	gc := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	ge := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	c := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	cr := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "created", "d1", 4)
	m := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "created"), "mut", "d1", 5)
	m.ExpectedRevision = 99
	r := Replay([]Event{gc, ge, c, cr, m})
	found := false
	for _, cf := range r.Conflicts {
		if cf.EventID == "mut" && strings.Contains(cf.Reason, "expected_revision") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected_revision mismatch not isolated: %+v", r.Conflicts)
	}
}

func TestExplicitMutationChainSurvivesReversedIDs(t *testing.T) {
	gc := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	ge := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	c := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "z-created", "d1", 4)
	m1 := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "z-created"), "a-mut", "d1", 5)
	m2 := withID(rawStatus("n1", StatusDone, 2, "l1", 1, "a-mut"), "b-mut", "d1", 6)
	m2.Payload = json.RawMessage(`{"node_id":"n1","status":"done","evidence_ids":["ev1"]}`)
	ev := Event{EventID: "0evidence", DeviceID: "d1", DeviceSeq: 7, Type: EvEvidenceAdded, CausalParents: []string{"z-created"}, Payload: json.RawMessage(`{"evidence_id":"ev1","node_id":"n1","kind":"test","sha256":"` + strings.Repeat("a", 64) + `"}`)}
	r := Replay([]Event{gc, ge, c, created, m1, m2, ev})
	if r.Tree.Nodes["n1"].Status != StatusDone || len(r.Conflicts) != 0 {
		t.Fatalf("explicit chain failed with reversed IDs: %+v", r)
	}
}

// Raw Append must not accept a mutation that omits the latest node event, nor a
// zero expected_revision wildcard. Rejected input must not create a file.
func TestRawAppendRejectsCausalBypassAndZeroExpectedRevision(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	goalClaim := mustAppend(t, s, withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1))
	goalCreated := mustAppend(t, s, withID(rawCreated("g1", "goal", "", "lg", 1, goalClaim.EventID), "gcreated", "d1", 2))
	claim := mustAppend(t, s, withID(rawOwn("n1", "l1", "a", 1, goalCreated.EventID), "claim", "d1", 3))
	mustAppend(t, s, withID(rawCreated("n1", "task", "g1", "l1", 1, claim.EventID), "created", "d1", 4))
	before, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	noChain := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "claim"), "no-chain", "d1", 5)
	if _, _, err := s.Append(noChain); err == nil {
		t.Fatal("raw append accepted a mutation without the latest node event")
	}
	zeroExpected := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "created"), "zero-exp", "d1", 5)
	zeroExpected.ExpectedRevision = 0
	if _, _, err := s.Append(zeroExpected); err == nil {
		t.Fatal("raw append accepted expected_revision=0")
	}
	after, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) || len(after.Quarantined) != 0 {
		t.Fatalf("rejected append wrote an event: before=%d after=%d quarantined=%+v", len(before.Events), len(after.Events), after.Quarantined)
	}
	good := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "created"), "good", "d1", 5)
	if _, _, err := s.Append(good); err != nil {
		t.Fatalf("valid chained mutation rejected: %v", err)
	}
}

// Multi-device causal chain with event IDs that sort in reverse order must
// still apply through explicit parents, never through ID/device_seq ordering.
func TestExplicitMutationChainAcrossDevicesReversedIDs(t *testing.T) {
	gc := withID(rawOwn("g1", "lg", "a", 1), "gclaim", "d1", 1)
	ge := withID(rawCreated("g1", "goal", "", "lg", 1, "gclaim"), "gcreated", "d1", 2)
	c := withID(rawOwn("n1", "l1", "a", 1, "gcreated"), "claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "claim"), "zz-created", "d1", 4)
	m1 := withID(rawStatus("n1", StatusInProgress, 1, "l1", 1, "zz-created"), "mm-mut", "d2", 1)
	ev := Event{EventID: "0evidence", DeviceID: "d1", DeviceSeq: 5, Type: EvEvidenceAdded, CausalParents: []string{"zz-created"}, Payload: json.RawMessage(`{"evidence_id":"ev1","node_id":"n1","kind":"test","sha256":"` + strings.Repeat("a", 64) + `"}`)}
	m2 := withID(rawStatus("n1", StatusDone, 2, "l1", 1, "mm-mut", "0evidence"), "aa-mut", "d2", 2)
	m2.Payload = json.RawMessage(`{"node_id":"n1","status":"done","evidence_ids":["ev1"]}`)
	r := Replay([]Event{gc, ge, c, created, m1, ev, m2})
	if r.Tree.Nodes["n1"].Status != StatusDone || len(r.Conflicts) != 0 || len(r.Pending) != 0 {
		t.Fatalf("multi-device reversed-ID chain failed: %+v", r)
	}
}

// A transaction batch is all-or-nothing. One bad member must quarantine the
// whole file, and a truncated/uncommitted batch must never contribute events.
func TestTransactionBatchIsAllOrNothing(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	e1 := withID(rawOwn("n1", "l1", "a", 1), "batch-a", "d1", 1)
	if err := e1.Seal(); err != nil {
		t.Fatal(err)
	}
	e2 := withID(rawOwn("n2", "l2", "a", 1), "batch-b", "d1", 2)
	if err := e2.Seal(); err != nil {
		t.Fatal(err)
	}
	e2.EventHash = "sha256:" + strings.Repeat("0", 64)
	b, _ := json.Marshal([]Event{e1, e2})
	if err := os.WriteFile(filepath.Join(ws, "events", "batch-bad.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 0 || len(r.Quarantined) != 1 {
		t.Fatalf("bad batch was partially applied: events=%d quarantined=%+v", len(r.Events), r.Quarantined)
	}
	if err := os.WriteFile(filepath.Join(ws, "events", "batch-partial.json"), []byte(`[{"event_id":"x"`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 0 || len(r.Quarantined) != 2 {
		t.Fatalf("truncated batch contributed events: events=%d quarantined=%+v", len(r.Events), r.Quarantined)
	}
}

func TestBatchManifestCountAndHashValidated(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	e1 := withID(rawOwn("manifest-a", "l1", "a", 1), "manifest-a", "d1", 1)
	if err := e1.Seal(); err != nil {
		t.Fatal(err)
	}
	e2 := withID(rawOwn("manifest-b", "l2", "a", 1), "manifest-b", "d1", 2)
	if err := e2.Seal(); err != nil {
		t.Fatal(err)
	}
	h, err := batchHash([]Event{e1, e2})
	if err != nil {
		t.Fatal(err)
	}
	env := batchEnvelope{AnchorBatch: 1, BatchID: "batch_manifest", EventCount: 2, EventsSHA256: h, Events: []Event{e1, e2}}
	write := func(env batchEnvelope) {
		b, _ := json.Marshal(env)
		if err := os.WriteFile(filepath.Join(ws, "events", "batch_manifest.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(env)
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 2 || len(r.Quarantined) != 0 {
		t.Fatalf("valid manifest rejected: %+v", r)
	}
	badCount := env
	badCount.EventCount = 1
	write(badCount)
	r, _ = s.LoadReport()
	if len(r.Events) != 0 || len(r.Quarantined) != 1 || !strings.Contains(r.Quarantined[0].Reason, "event_count") {
		t.Fatalf("manifest count mismatch not quarantined: %+v", r)
	}
	badHash := env
	badHash.EventsSHA256 = "sha256:" + strings.Repeat("0", 64)
	write(badHash)
	r, _ = s.LoadReport()
	if len(r.Events) != 0 || len(r.Quarantined) != 1 || !strings.Contains(r.Quarantined[0].Reason, "manifest hash") {
		t.Fatalf("manifest hash mismatch not quarantined: %+v", r)
	}
	dup := env
	dup.Events = []Event{e1, e1}
	dup.EventCount = 2
	dup.EventsSHA256, _ = batchHash(dup.Events)
	write(dup)
	r, _ = s.LoadReport()
	if len(r.Events) != 0 || len(r.Quarantined) != 1 || !strings.Contains(r.Quarantined[0].Reason, "duplicate") {
		t.Fatalf("duplicate batch event not quarantined: %+v", r)
	}
}

// A manifested batch must not be downgraded to a legacy bare array or single
// object. New workspace metadata pins the strict event-log version; legacy
// formats are read-only audit diagnostics, never silently applied.
func TestTamperedEnvelopeCannotDowngradeToLegacy(t *testing.T) {
	ws := t.TempDir()
	manifest := `{"schema_version":1,"event_log_version":2,"workspace_id":"ws1","device_id":"d1"}`
	if err := os.WriteFile(filepath.Join(ws, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	e1 := withID(rawOwn("down-a", "l1", "a", 1), "down-a", "d1", 1)
	e2 := withID(rawOwn("down-b", "l2", "a", 1), "down-b", "d1", 2)
	if _, err := s.Transact(func(tx *Tx) error {
		if _, err := tx.Add(e1); err != nil {
			return err
		}
		_, err := tx.Add(e2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(ws, "events", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("batch file not found: %v %v", paths, err)
	}
	original, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var env batchEnvelope
	if err := json.Unmarshal(original, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Events) != 2 {
		t.Fatalf("unexpected batch: %+v", env)
	}
	write := func(b []byte) {
		if err := os.WriteFile(paths[0], b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertQuarantined := func(want string) {
		t.Helper()
		r, err := s.LoadReport()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Events) != 0 || len(r.Quarantined) != 1 {
			t.Fatalf("tampered batch partially applied: events=%d quarantined=%+v", len(r.Events), r.Quarantined)
		}
		if !strings.Contains(r.Quarantined[0].Reason, want) {
			t.Fatalf("quarantine reason %q missing %q", r.Quarantined[0].Reason, want)
		}
	}
	bare, _ := json.Marshal([]Event{env.Events[0]})
	write(bare)
	assertQuarantined("legacy bare event array")
	audit, err := s.AuditEventFormats()
	if err != nil {
		t.Fatal(err)
	}
	if !audit.ReadOnly || audit.Writes || !audit.MigrationDryRunOnly || len(audit.LegacyFiles) != 1 {
		t.Fatalf("legacy audit wrong: %+v", audit)
	}
	single, _ := json.Marshal(env.Events[0])
	write(single)
	assertQuarantined("legacy single-event object")
	truncated := env
	truncated.Events = env.Events[:1]
	b, _ := json.Marshal(truncated)
	write(b)
	assertQuarantined("event_count mismatch")
	write(original)
	r, err := s.LoadReport()
	if err != nil || len(r.Events) != 2 || len(r.Quarantined) != 0 {
		t.Fatalf("restored batch not readable: %+v %v", r, err)
	}
}
