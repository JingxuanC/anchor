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
	mut := withID(rawStatus("n1", StatusInProgress, 1, "l-new", 3, "takeover"), "mut", "d2", 2)
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
	tomb := Event{EventID: "tomb", DeviceID: "d1", DeviceSeq: 5, Type: EvNodeTombstoned, CausalParents: []string{"claim"}, BaseRevision: 1, LeaseID: "l1", FencingToken: 1, Payload: json.RawMessage(`{"node_id":"n1"}`)}
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
