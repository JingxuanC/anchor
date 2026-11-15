package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func mustAppend(t *testing.T, s *Store, e Event) Event {
	t.Helper()
	got, created, err := s.Append(e)
	if err != nil {
		t.Fatalf("append %s: %v", e.Type, err)
	}
	if !created && e.IdempotencyKey == "" {
		t.Fatalf("event not created")
	}
	return got
}

func own(node, lease, owner string, token uint64, parents ...string) Event {
	p := OwnershipPayload{NodeID: node, Owner: owner, LeaseID: lease, FencingToken: token}
	b, _ := json.Marshal(p)
	return Event{Type: EvOwnershipClaimed, CausalParents: parents, LeaseID: lease, FencingToken: token, Payload: b}
}
func ownType(t EventType, node, lease, owner string, token uint64, parents ...string) Event {
	e := own(node, lease, owner, token, parents...)
	e.Type = t
	return e
}
func nodeCreate(node, typ, title, parent, lease string, token uint64, parents ...string) Event {
	p := NodePayload{NodeID: node, Type: typ, Title: title, ParentID: parent, Owner: "agent"}
	b, _ := json.Marshal(p)
	return Event{Type: EvNodeCreated, CausalParents: parents, LeaseID: lease, FencingToken: token, Payload: b}
}
func status(node string, to Status, base int, lease string, token uint64, parents ...string) Event {
	p := NodePayload{NodeID: node, Status: string(to)}
	b, _ := json.Marshal(p)
	return Event{Type: EvStatusChanged, CausalParents: parents, BaseRevision: base, ExpectedRevision: base + 1, LeaseID: lease, FencingToken: token, Payload: b}
}

func seed(t *testing.T, s *Store) (claim Event, created Event) {
	goalClaim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	goalCreated := mustAppend(t, s, nodeCreate("g1", "goal", "goal", "", "lg", 1, goalClaim.EventID))
	claim = mustAppend(t, s, own("n1", "l1", "agent", 1, goalCreated.EventID))
	created = mustAppend(t, s, nodeCreate("n1", "task", "task", "g1", "l1", 1, claim.EventID))
	return
}

func TestDeterministicReplayIndependentOfSliceOrder(t *testing.T) {
	s, _ := Open(t.TempDir())
	claim, created := seed(t, s)
	hb := mustAppend(t, s, ownType(EvOwnershipHeartbeat, "n1", "l1", "agent", 1, claim.EventID))
	mustAppend(t, s, status("n1", StatusInProgress, 1, "l1", 1, created.EventID, hb.EventID))
	events, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(Replay(events))
	rev := append([]Event(nil), events...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	b, _ := json.Marshal(Replay(rev))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("replay not deterministic\n%s\n%s", a, b)
	}
	_ = claim
	_ = created
}

func TestMissingParentStagesThenReplays(t *testing.T) {
	s, _ := Open(t.TempDir())
	claim, created := seed(t, s)
	mut := status("n1", StatusInProgress, 1, "l1", 1, created.EventID, "missing_ownership")
	mut.EventID = "mut"
	mut.DeviceID = "d1"
	mut.DeviceSeq = 10
	events := append(eventsOrFail(t, s), mut)
	r := Replay(events)
	if r.RecoveryStatus != "incomplete" || len(r.Pending) != 1 {
		t.Fatalf("expected staged mutation, got %+v", r)
	}
	if r.Tree.Nodes["n1"].Status != StatusTodo {
		t.Fatal("staged mutation must not apply")
	}
	// Parent arrives; the mutation now replays from the explicit chain.
	ownEvent := own("n1", "l1", "agent", 1, claim.EventID)
	ownEvent.EventID = "missing_ownership"
	ownEvent.DeviceID = "d1"
	ownEvent.DeviceSeq = 11
	events = append(events, ownEvent)
	r = Replay(events)
	if len(r.Pending) != 0 || r.Tree.Nodes["n1"].Status != StatusInProgress {
		t.Fatalf("expected replay after parent arrival, got %+v", r)
	}
}

func TestConcurrentOwnershipIsolatesWholeMutation(t *testing.T) {
	s, _ := Open(t.TempDir())
	claim, created := seed(t, s)
	events := eventsOrFail(t, s)
	mut := status("n1", StatusInProgress, 1, "l1", 1, created.EventID)
	mut.EventID = "mut"
	mut.DeviceID = "d1"
	mut.DeviceSeq = 10
	to := ownType(EvOwnershipTakenOver, "n1", "l2", "other", 2, claim.EventID)
	to.EventID = "takeover"
	to.DeviceID = "d1"
	to.DeviceSeq = 11
	r := Replay(append(events, mut, to))
	if len(r.Conflicts) == 0 {
		t.Fatalf("expected conflict, got %+v", r)
	}
	if r.Tree.Nodes["n1"].Status != StatusTodo {
		t.Fatal("whole mutation must be isolated")
	}
	found := false
	for _, c := range r.Conflicts {
		if c.EventID == mut.EventID {
			found = true
		}
	}
	if !found {
		t.Fatalf("mutation not in conflicts: %+v", r.Conflicts)
	}
}

func TestSameRevisionConcurrentMutationsBothIsolate(t *testing.T) {
	s, _ := Open(t.TempDir())
	_, created := seed(t, s)
	events := eventsOrFail(t, s)
	a := status("n1", StatusInProgress, 1, "l1", 1, created.EventID)
	a.EventID = "mut-a"
	a.DeviceID = "d1"
	a.DeviceSeq = 10
	b := status("n1", StatusBlocked, 1, "l1", 1, created.EventID)
	b.EventID = "mut-b"
	b.DeviceID = "d1"
	b.DeviceSeq = 11
	r := Replay(append(events, a, b))
	seen := map[string]bool{}
	for _, c := range r.Conflicts {
		seen[c.EventID] = true
	}
	if !seen[a.EventID] || !seen[b.EventID] {
		t.Fatalf("both mutations must conflict, got %+v", r.Conflicts)
	}
	if r.Tree.Nodes["n1"].Status != StatusTodo {
		t.Fatal("no mutation should have applied")
	}
}

func TestDeviceSeqMonotonic(t *testing.T) {
	s, _ := Open(t.TempDir())
	e := own("n1", "l1", "agent", 1)
	e.DeviceID = "d1"
	e.DeviceSeq = 2
	if _, _, err := s.Append(e); err == nil {
		t.Fatal("expected device_seq gap to fail")
	}
	e.DeviceSeq = 0
	if _, _, err := s.Append(e); err != nil {
		t.Fatalf("auto seq failed: %v", err)
	}
	e2 := own("n2", "l2", "agent", 1)
	if _, _, err := s.Append(e2); err != nil {
		t.Fatalf("next seq failed: %v", err)
	}
}

func eventsOrFail(t *testing.T, s *Store) []Event {
	t.Helper()
	events, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	return events
}
