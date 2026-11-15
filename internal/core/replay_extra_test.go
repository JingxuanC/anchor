package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func adapterPayloadFor(adapter, node, label, hook string, seq uint64) AdapterSessionPayload {
	ref := hashRefFor("session|" + label)
	return AdapterSessionPayload{
		SessionID:      AdapterSessionID(adapter, ref),
		SessionRef:     ref,
		NodeID:         node,
		Adapter:        adapter,
		HookEvent:      hook,
		Tool:           "Bash",
		Timestamp:      "2026-01-01T00:00:00Z",
		Source:         "fixture",
		TrustLevel:     "unverified_adapter",
		SourceEventRef: hashRefFor("source|" + label + "|" + hook),
		SourceSeq:      seq,
	}
}

func adapterSessionEvent(t *testing.T, typ EventType, id string, seq uint64, p AdapterSessionPayload, parents ...string) Event {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return withID(Event{Type: typ, CausalParents: parents, Payload: b}, id, "d1", seq)
}

func sessionReplayBase() []Event {
	return []Event{
		withID(rawOwn("g1", "lg", "a", 1), "s-gclaim", "d1", 1),
		withID(rawCreated("g1", "goal", "", "lg", 1, "s-gclaim"), "s-gcreated", "d1", 2),
	}
}

func conflictFor(r ReplayResult, id string) *Conflict {
	for i := range r.Conflicts {
		if r.Conflicts[i].EventID == id {
			return &r.Conflicts[i]
		}
	}
	return nil
}

func requireConflict(t *testing.T, r ReplayResult, id, want string) {
	t.Helper()
	c := conflictFor(r, id)
	if c == nil {
		t.Fatalf("event %s not in conflicts: %+v", id, r.Conflicts)
	}
	if !strings.Contains(c.Reason, want) {
		t.Fatalf("conflict for %s = %q, want it to contain %q", id, c.Reason, want)
	}
}

func requireNoConflicts(t *testing.T, r ReplayResult) {
	t.Helper()
	if len(r.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", r.Conflicts)
	}
	if r.RecoveryStatus != "complete" {
		t.Fatalf("projection incomplete: pending=%v conflicts=%+v", r.Pending, r.Conflicts)
	}
}

func TestAdapterSessionLifecycle(t *testing.T) {
	pSeen := adapterPayloadFor("claude-code", "g1", "s1", "SessionStart", 0)
	seen := adapterSessionEvent(t, EvAdapterSessionSeen, "sess-seen", 3, pSeen, "s-gcreated")
	pTool := adapterPayloadFor("claude-code", "g1", "s1", "PreToolUse", 5)
	seen2 := adapterSessionEvent(t, EvAdapterSessionSeen, "sess-seen-2", 4, pTool, "sess-seen")
	pEnd := adapterPayloadFor("claude-code", "g1", "s1", "SessionEnd", 6)
	end := adapterSessionEvent(t, EvAdapterSessionEnded, "sess-end", 5, pEnd, "sess-seen-2")

	r := Replay(append(sessionReplayBase(), seen, seen2, end))
	requireNoConflicts(t, r)
	sess, ok := r.Tree.Sessions[pSeen.SessionID]
	if !ok {
		t.Fatalf("session %s missing from projection", pSeen.SessionID)
	}
	if sess.ID != pSeen.SessionID || sess.Adapter != "claude-code" || sess.NodeID != "g1" {
		t.Fatalf("session identity not recorded: %+v", sess)
	}
	if sess.SessionRef != pSeen.SessionRef || sess.Source != "fixture" || sess.TrustLevel != "unverified_adapter" {
		t.Fatalf("session metadata not recorded: %+v", sess)
	}
	if sess.FirstEventID != seen.EventID {
		t.Fatalf("first_event_id = %q, want %q", sess.FirstEventID, seen.EventID)
	}
	if !sess.Ended || sess.EndEventID != end.EventID {
		t.Fatalf("end state not recorded: %+v", sess)
	}
	if sess.LastEventID != end.EventID || sess.LastHookEvent != "SessionEnd" || sess.LastTimestamp != pEnd.Timestamp {
		t.Fatalf("last observation not updated by end: %+v", sess)
	}
	if sess.LastSourceSeq != 6 || sess.LastSourceEventRef != pEnd.SourceEventRef {
		t.Fatalf("last source sequence not updated: %+v", sess)
	}
	if sess.HistoricalObservations != 0 {
		t.Fatalf("unexpected historical observations: %+v", sess)
	}
	// Sessions are metadata only: the node keeps its initial projection.
	if n := r.Tree.Nodes["g1"]; n == nil || n.Revision != 1 || n.Status != StatusInProgress {
		t.Fatalf("session events mutated the node: %+v", n)
	}
}

func TestAdapterSessionHistoricalObservationAfterEnd(t *testing.T) {
	pSeen := adapterPayloadFor("claude-code", "g1", "s2", "SessionStart", 1)
	seen := adapterSessionEvent(t, EvAdapterSessionSeen, "hist-seen", 3, pSeen, "s-gcreated")
	pEnd := adapterPayloadFor("claude-code", "g1", "s2", "SessionEnd", 2)
	end := adapterSessionEvent(t, EvAdapterSessionEnded, "hist-end", 4, pEnd, "hist-seen")
	pLate := adapterPayloadFor("claude-code", "g1", "s2", "HistoricalHook", 1)
	late := adapterSessionEvent(t, EvAdapterSessionSeen, "hist-late", 5, pLate, "hist-end")

	r := Replay(append(sessionReplayBase(), seen, end, late))
	requireNoConflicts(t, r)
	sess := r.Tree.Sessions[pSeen.SessionID]
	if !sess.Ended || sess.HistoricalObservations != 1 {
		t.Fatalf("historical observation not counted: %+v", sess)
	}
	if sess.LastEventID != end.EventID || sess.LastSourceSeq != 2 || sess.LastHookEvent != "SessionEnd" {
		t.Fatalf("historical observation moved last_*: %+v", sess)
	}
}

func TestAdapterSessionHistoricalObservationBeforeEnd(t *testing.T) {
	pSeen := adapterPayloadFor("claude-code", "g1", "s3", "LatestHook", 2)
	seen := adapterSessionEvent(t, EvAdapterSessionSeen, "pre-seen", 3, pSeen, "s-gcreated")
	pOld := adapterPayloadFor("claude-code", "g1", "s3", "OldHook", 1)
	old := adapterSessionEvent(t, EvAdapterSessionSeen, "pre-old", 4, pOld, "pre-seen")

	r := Replay(append(sessionReplayBase(), seen, old))
	requireNoConflicts(t, r)
	sess := r.Tree.Sessions[pSeen.SessionID]
	if sess.HistoricalObservations != 1 {
		t.Fatalf("historical observation not counted: %+v", sess)
	}
	if sess.LastEventID != seen.EventID || sess.LastSourceSeq != 2 || sess.LastHookEvent != "LatestHook" {
		t.Fatalf("historical observation moved last_*: %+v", sess)
	}
	if sess.Ended {
		t.Fatalf("unsequenced history must not end the session: %+v", sess)
	}
}

func TestAdapterSessionSequenceCollision(t *testing.T) {
	p := adapterPayloadFor("claude-code", "g1", "s4", "HookA", 5)
	first := adapterSessionEvent(t, EvAdapterSessionSeen, "coll-seen", 3, p, "s-gcreated")
	second := adapterSessionEvent(t, EvAdapterSessionSeen, "coll-dup", 4, p, "coll-seen")
	r := Replay(append(sessionReplayBase(), first, second))
	requireConflict(t, r, "coll-dup", "collides with latest observation")
	sess := r.Tree.Sessions[p.SessionID]
	if sess.LastSourceSeq != 5 || sess.LastEventID != first.EventID {
		t.Fatalf("colliding observation changed the session: %+v", sess)
	}
}

func TestAdapterSessionUnsequencedAfterSequenced(t *testing.T) {
	p := adapterPayloadFor("claude-code", "g1", "s5", "HookA", 3)
	seen := adapterSessionEvent(t, EvAdapterSessionSeen, "mix-seen", 3, p, "s-gcreated")
	pUnsequenced := adapterPayloadFor("claude-code", "g1", "s5", "HookB", 0)
	unsequenced := adapterSessionEvent(t, EvAdapterSessionSeen, "mix-unsequenced", 4, pUnsequenced, "mix-seen")
	r := Replay(append(sessionReplayBase(), seen, unsequenced))
	requireConflict(t, r, "mix-unsequenced", "sequenced session requires source_seq")
}

func TestAdapterSessionLateObservationRejections(t *testing.T) {
	t.Run("newer sequence after end", func(t *testing.T) {
		pSeen := adapterPayloadFor("claude-code", "g1", "s6", "SessionStart", 1)
		seen := adapterSessionEvent(t, EvAdapterSessionSeen, "late-seen", 3, pSeen, "s-gcreated")
		pEnd := adapterPayloadFor("claude-code", "g1", "s6", "SessionEnd", 2)
		end := adapterSessionEvent(t, EvAdapterSessionEnded, "late-end", 4, pEnd, "late-seen")
		pLate := adapterPayloadFor("claude-code", "g1", "s6", "LateHook", 3)
		late := adapterSessionEvent(t, EvAdapterSessionSeen, "late-observed", 5, pLate, "late-end")
		r := Replay(append(sessionReplayBase(), seen, end, late))
		requireConflict(t, r, "late-observed", "late observation after session_end (source_seq 3 > 2)")
	})
	t.Run("unsequenced after unsequenced end", func(t *testing.T) {
		pSeen := adapterPayloadFor("claude-code", "g1", "s7", "SessionStart", 0)
		seen := adapterSessionEvent(t, EvAdapterSessionSeen, "unseq-seen", 3, pSeen, "s-gcreated")
		pEnd := adapterPayloadFor("claude-code", "g1", "s7", "SessionEnd", 0)
		end := adapterSessionEvent(t, EvAdapterSessionEnded, "unseq-end", 4, pEnd, "unseq-seen")
		pLate := adapterPayloadFor("claude-code", "g1", "s7", "LateHook", 0)
		late := adapterSessionEvent(t, EvAdapterSessionSeen, "unseq-late", 5, pLate, "unseq-end")
		r := Replay(append(sessionReplayBase(), seen, end, late))
		requireConflict(t, r, "unseq-late", "has no source_seq")
	})
}

func TestAdapterSessionEndSequenceRules(t *testing.T) {
	t.Run("end requires source_seq after sequenced observations", func(t *testing.T) {
		pSeen := adapterPayloadFor("claude-code", "g1", "s8", "SessionStart", 1)
		seen := adapterSessionEvent(t, EvAdapterSessionSeen, "end-seq-seen", 3, pSeen, "s-gcreated")
		pEnd := adapterPayloadFor("claude-code", "g1", "s8", "SessionEnd", 0)
		end := adapterSessionEvent(t, EvAdapterSessionEnded, "end-no-seq", 4, pEnd, "end-seq-seen")
		r := Replay(append(sessionReplayBase(), seen, end))
		requireConflict(t, r, "end-no-seq", "session_end requires source_seq")
	})
	t.Run("end sequence must be newer", func(t *testing.T) {
		pSeen := adapterPayloadFor("claude-code", "g1", "s9", "SessionStart", 5)
		seen := adapterSessionEvent(t, EvAdapterSessionSeen, "end-new-seen", 3, pSeen, "s-gcreated")
		pEnd := adapterPayloadFor("claude-code", "g1", "s9", "SessionEnd", 5)
		end := adapterSessionEvent(t, EvAdapterSessionEnded, "end-same-seq", 4, pEnd, "end-new-seen")
		r := Replay(append(sessionReplayBase(), seen, end))
		requireConflict(t, r, "end-same-seq", "is not newer than latest observed")
	})
	t.Run("end without observation", func(t *testing.T) {
		pEnd := adapterPayloadFor("claude-code", "g1", "s10", "SessionEnd", 1)
		end := adapterSessionEvent(t, EvAdapterSessionEnded, "end-orphan", 3, pEnd, "s-gcreated")
		r := Replay(append(sessionReplayBase(), end))
		requireConflict(t, r, "end-orphan", "has no observed event")
	})
	t.Run("double end", func(t *testing.T) {
		pSeen := adapterPayloadFor("claude-code", "g1", "s11", "SessionStart", 1)
		seen := adapterSessionEvent(t, EvAdapterSessionSeen, "double-seen", 3, pSeen, "s-gcreated")
		pEnd := adapterPayloadFor("claude-code", "g1", "s11", "SessionEnd", 2)
		end := adapterSessionEvent(t, EvAdapterSessionEnded, "double-end-1", 4, pEnd, "double-seen")
		pEnd2 := adapterPayloadFor("claude-code", "g1", "s11", "SessionEnd", 3)
		end2 := adapterSessionEvent(t, EvAdapterSessionEnded, "double-end-2", 5, pEnd2, "double-end-1")
		r := Replay(append(sessionReplayBase(), seen, end, end2))
		requireConflict(t, r, "double-end-2", "is already ended")
	})
}

func TestAdapterSessionPayloadValidationInReplay(t *testing.T) {
	t.Run("session id mismatch", func(t *testing.T) {
		p := adapterPayloadFor("claude-code", "g1", "s12", "SessionStart", 0)
		p.SessionID = AdapterSessionID("codex", p.SessionRef)
		bad := adapterSessionEvent(t, EvAdapterSessionSeen, "mismatch-id", 3, p, "s-gcreated")
		r := Replay(append(sessionReplayBase(), bad))
		requireConflict(t, r, "mismatch-id", "session_id does not match adapter/session_ref")
	})
	t.Run("missing node", func(t *testing.T) {
		p := adapterPayloadFor("claude-code", "ghost", "s13", "SessionStart", 0)
		bad := adapterSessionEvent(t, EvAdapterSessionSeen, "missing-node-session", 3, p, "s-gcreated")
		r := Replay(append(sessionReplayBase(), bad))
		requireConflict(t, r, "missing-node-session", "not found")
	})
	t.Run("tombstoned node", func(t *testing.T) {
		tomb := withID(Event{
			Type: EvNodeTombstoned, CausalParents: []string{"s-gcreated"},
			BaseRevision: 1, ExpectedRevision: 2, LeaseID: "lg", FencingToken: 1,
			Payload: json.RawMessage(`{"node_id":"g1"}`),
		}, "s-g1-tomb", "d1", 3)
		p := adapterPayloadFor("claude-code", "g1", "s14", "SessionStart", 0)
		bad := adapterSessionEvent(t, EvAdapterSessionSeen, "tomb-session", 4, p, "s-g1-tomb")
		r := Replay(append(sessionReplayBase(), tomb, bad))
		requireConflict(t, r, "tomb-session", "tombstoned")
	})
	t.Run("unknown adapter and hook event are accepted", func(t *testing.T) {
		p := adapterPayloadFor("future-adapter", "g1", "s15", "FutureHookEvent", 0)
		e := adapterSessionEvent(t, EvAdapterSessionSeen, "future-session", 3, p, "s-gcreated")
		r := Replay(append(sessionReplayBase(), e))
		requireNoConflicts(t, r)
		sess, ok := r.Tree.Sessions[p.SessionID]
		if !ok {
			t.Fatal("unknown adapter session not recorded")
		}
		if sess.Adapter != "future-adapter" || sess.LastHookEvent != "FutureHookEvent" {
			t.Fatalf("unknown adapter metadata not recorded: %+v", sess)
		}
	})
}

// mutationFixture is a committed goal + task with matching ownership so tests
// can add mutation events on top without repeating the causal chain.
type mutationFixture struct {
	events  []Event
	created Event
	claim   Event
}

func newMutationFixture() mutationFixture {
	gc := withID(rawOwn("g1", "lg", "a", 1), "f-gclaim", "d1", 1)
	ge := withID(rawCreated("g1", "goal", "", "lg", 1, "f-gclaim"), "f-gcreated", "d1", 2)
	claim := withID(rawOwn("n1", "l1", "a", 1, "f-gcreated"), "f-claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "f-claim"), "f-created", "d1", 4)
	return mutationFixture{events: []Event{gc, ge, claim, created}, created: created, claim: claim}
}

func TestMutationCausalAndRevisionRejections(t *testing.T) {
	tests := []struct {
		name  string
		build func(f mutationFixture) Event
		want  string
	}{
		{
			name: "missing latest causal parent",
			build: func(f mutationFixture) Event {
				// Causally ordered after the claim but not after node_created, so
				// this is not an ownership concurrency conflict; the mutation
				// itself must name the latest node event. The ID sorts after
				// f-created so node_created is projected before this event.
				return withID(status("n1", StatusInProgress, 1, "l1", 1, f.claim.EventID), "z-bad-causal", "d1", 5)
			},
			want: "must name latest node event",
		},
		{
			name: "base revision conflict",
			build: func(f mutationFixture) Event {
				return withID(status("n1", StatusInProgress, 99, "l1", 1, f.created.EventID), "bad-base", "d1", 5)
			},
			want: "revision conflict",
		},
		{
			name: "zero expected revision is not a wildcard",
			build: func(f mutationFixture) Event {
				e := status("n1", StatusInProgress, 1, "l1", 1, f.created.EventID)
				e.ExpectedRevision = 0
				return withID(e, "bad-expected-zero", "d1", 5)
			},
			want: "expected_revision conflict",
		},
		{
			name: "expected revision too high",
			build: func(f mutationFixture) Event {
				e := status("n1", StatusInProgress, 1, "l1", 1, f.created.EventID)
				e.ExpectedRevision = 9
				return withID(e, "bad-expected-high", "d1", 5)
			},
			want: "expected_revision conflict",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationFixture()
			e := tc.build(f)
			r := Replay(append(f.events, e))
			requireConflict(t, r, e.EventID, tc.want)
			if r.Tree.Nodes["n1"].Revision != 1 || r.Tree.Nodes["n1"].Status != StatusTodo {
				t.Fatalf("rejected mutation was applied: %+v", r.Tree.Nodes["n1"])
			}
		})
	}
	t.Run("mutation for unknown node", func(t *testing.T) {
		f := newMutationFixture()
		ghostClaim := withID(rawOwn("ghost", "lghost", "a", 1, "f-gcreated"), "f-ghost-claim", "d1", 5)
		mut := withID(status("ghost", StatusInProgress, 1, "lghost", 1, "f-ghost-claim"), "ghost-mut", "d1", 6)
		r := Replay(append(append(f.events, ghostClaim), mut))
		requireConflict(t, r, "ghost-mut", "node ghost not found")
	})
}

func TestMutationTransitionAndDoneRules(t *testing.T) {
	t.Run("illegal goal transition", func(t *testing.T) {
		gc := withID(rawOwn("g1", "lg", "a", 1), "t-gclaim", "d1", 1)
		ge := withID(rawCreated("g1", "goal", "", "lg", 1, "t-gclaim"), "t-gcreated", "d1", 2)
		done := withID(status("g1", StatusDone, 1, "lg", 1, "t-gcreated"), "goal-done", "d1", 3)
		r := Replay([]Event{gc, ge, done})
		requireConflict(t, r, "goal-done", "illegal transition")
		if r.Tree.Nodes["g1"].Status != StatusInProgress {
			t.Fatalf("illegal transition changed status: %+v", r.Tree.Nodes["g1"])
		}
	})
	t.Run("done without evidence", func(t *testing.T) {
		f := newMutationFixture()
		inProgress := withID(status("n1", StatusInProgress, 1, "l1", 1, f.created.EventID), "done-in-progress", "d1", 5)
		done := withID(status("n1", StatusDone, 2, "l1", 1, "done-in-progress"), "done-no-evidence", "d1", 6)
		r := Replay(append(append(f.events, inProgress), done))
		requireConflict(t, r, "done-no-evidence", "done requires evidence_ids")
	})
	t.Run("done with user override", func(t *testing.T) {
		f := newMutationFixture()
		inProgress := withID(status("n1", StatusInProgress, 1, "l1", 1, f.created.EventID), "override-in-progress", "d1", 5)
		payload, _ := json.Marshal(NodePayload{NodeID: "n1", Status: string(StatusDone), UserOverride: true})
		done := withID(Event{
			Type: EvStatusChanged, CausalParents: []string{"override-in-progress"},
			BaseRevision: 2, ExpectedRevision: 3, LeaseID: "l1", FencingToken: 1, Payload: payload,
		}, "override-done", "d1", 6)
		r := Replay(append(append(f.events, inProgress), done))
		requireConflict(t, r, "override-done", "user_override is disabled")
	})
	t.Run("note_updated requires a note node", func(t *testing.T) {
		f := newMutationFixture()
		payload, _ := json.Marshal(NodePayload{NodeID: "n1", Note: "hello"})
		noteUpdate := withID(Event{
			Type: EvNoteUpdated, CausalParents: []string{f.created.EventID},
			BaseRevision: 1, ExpectedRevision: 2, LeaseID: "l1", FencingToken: 1, Payload: payload,
		}, "note-on-task", "d1", 5)
		r := Replay(append(f.events, noteUpdate))
		requireConflict(t, r, "note-on-task", "note_updated requires a note node")
	})
}

func TestMutationNodeUpdatedAndNoteFields(t *testing.T) {
	f := newMutationFixture()
	t.Run("goal detail fields update", func(t *testing.T) {
		confidence := 0.75
		payload, _ := json.Marshal(NodePayload{
			NodeID: "g1", Title: "goal v2", NorthStar: "north star v2",
			Must: []string{"local only"}, MustNot: []string{"no cloud"},
			SuccessCriteria: []string{"resume"}, Provenance: string(ProvenanceAgentInferred),
			Confidence: &confidence,
		})
		update := withID(Event{
			Type: EvNodeUpdated, CausalParents: []string{"f-gcreated"},
			BaseRevision: 1, ExpectedRevision: 2, LeaseID: "lg", FencingToken: 1, Payload: payload,
		}, "goal-update", "d1", 5)
		r := Replay(append(append([]Event{}, f.events...), update))
		requireNoConflicts(t, r)
		n := r.Tree.Nodes["g1"]
		if n.Title != "goal v2" || n.NorthStar != "north star v2" {
			t.Fatalf("goal fields not updated: %+v", n)
		}
		if len(n.Must) != 1 || len(n.MustNot) != 1 || len(n.SuccessCriteria) != 1 {
			t.Fatalf("goal lists not updated: %+v", n)
		}
		if n.Provenance != ProvenanceAgentInferred || n.Confidence == nil || *n.Confidence != 0.75 {
			t.Fatalf("provenance/confidence not updated: %+v", n)
		}
		if n.Revision != 2 || n.LastEventID != "goal-update" {
			t.Fatalf("revision bookkeeping wrong: %+v", n)
		}
	})
	t.Run("decision detail fields update", func(t *testing.T) {
		decisionClaim := withID(rawOwn("d1n", "ld", "a", 1, "f-gcreated"), "decision-claim", "d1", 5)
		decisionCreated := withID(rawCreated("d1n", "decision", "g1", "ld", 1, "decision-claim"), "decision-created", "d1", 6)
		payload, _ := json.Marshal(NodePayload{
			NodeID: "d1n", Decision: "hash bodies", Rationale: "tamper detection",
			Alternatives: []string{"signing"}, Consequences: []string{"no keys"},
		})
		update := withID(Event{
			Type: EvNodeUpdated, CausalParents: []string{"decision-created"},
			BaseRevision: 1, ExpectedRevision: 2, LeaseID: "ld", FencingToken: 1, Payload: payload,
		}, "decision-update", "d1", 7)
		r := Replay(append(append(append([]Event{}, f.events...), decisionClaim), decisionCreated, update))
		requireNoConflicts(t, r)
		n := r.Tree.Nodes["d1n"]
		if n.Decision != "hash bodies" || n.Rationale != "tamper detection" {
			t.Fatalf("decision fields not updated: %+v", n)
		}
		if len(n.Alternatives) != 1 || len(n.Consequences) != 1 {
			t.Fatalf("decision lists not updated: %+v", n)
		}
	})
	t.Run("note update applies to note nodes", func(t *testing.T) {
		noteClaim := withID(rawOwn("note1", "ln", "a", 1, "f-gcreated"), "note-claim", "d1", 5)
		noteCreated := withID(rawCreated("note1", "note", "g1", "ln", 1, "note-claim"), "note-created", "d1", 6)
		payload, _ := json.Marshal(NodePayload{NodeID: "note1", Note: "durable note"})
		update := withID(Event{
			Type: EvNoteUpdated, CausalParents: []string{"note-created"},
			BaseRevision: 1, ExpectedRevision: 2, LeaseID: "ln", FencingToken: 1, Payload: payload,
		}, "note-update", "d1", 7)
		r := Replay(append(append(append([]Event{}, f.events...), noteClaim), noteCreated, update))
		requireNoConflicts(t, r)
		n := r.Tree.Nodes["note1"]
		if n.Note != "durable note" || n.Status != StatusActive || n.Revision != 2 {
			t.Fatalf("note update failed: %+v", n)
		}
	})
	t.Run("tombstone applies and is terminal", func(t *testing.T) {
		tomb := withID(Event{
			Type: EvNodeTombstoned, CausalParents: []string{f.created.EventID},
			BaseRevision: 1, ExpectedRevision: 2, LeaseID: "l1", FencingToken: 1,
			Payload: json.RawMessage(`{"node_id":"n1"}`),
		}, "tomb-apply", "d1", 5)
		r := Replay(append(f.events, tomb))
		requireNoConflicts(t, r)
		n := r.Tree.Nodes["n1"]
		if !n.Deleted || n.Revision != 2 || n.LastEventID != "tomb-apply" {
			t.Fatalf("tombstone not applied: %+v", n)
		}
	})
}

func TestOwnershipLifecycleReplay(t *testing.T) {
	gc := withID(rawOwn("g1", "lg", "a", 1), "ol-gclaim", "d1", 1)
	ge := withID(rawCreated("g1", "goal", "", "lg", 1, "ol-gclaim"), "ol-gcreated", "d1", 2)
	claim := withID(rawOwn("n1", "l1", "agent", 1, "ol-gcreated"), "ol-claim", "d1", 3)
	created := withID(rawCreated("n1", "task", "g1", "l1", 1, "ol-claim"), "ol-created", "d1", 4)
	hb := withID(ownType(EvOwnershipHeartbeat, "n1", "l1", "agent", 1, "ol-claim"), "ol-hb", "d1", 5)
	release := withID(ownType(EvOwnershipReleased, "n1", "l1", "", 1, "ol-hb"), "ol-release", "d1", 6)
	takeover := withID(ownType(EvOwnershipTakenOver, "n1", "l2", "other", 2, "ol-release"), "ol-takeover", "d1", 7)
	mut := withID(status("n1", StatusInProgress, 1, "l2", 2, "ol-created", "ol-takeover"), "ol-mut", "d1", 8)
	r := Replay([]Event{gc, ge, claim, created, hb, release, takeover, mut})
	requireNoConflicts(t, r)
	n := r.Tree.Nodes["n1"]
	if n.Status != StatusInProgress || n.Revision != 2 {
		t.Fatalf("mutation after takeover not applied: %+v", n)
	}
	if n.Owner != "other" || n.LeaseID != "l2" || n.FencingToken != 2 {
		t.Fatalf("takeover ownership not projected: %+v", n)
	}
	if n.LeaseEventID != "ol-takeover" {
		t.Fatalf("lease event = %q, want ol-takeover", n.LeaseEventID)
	}
	// The released state is not resurrected by a stale lease.
	stale := withID(status("n1", StatusBlocked, 1, "l1", 1, "ol-created", "ol-takeover"), "ol-stale", "d1", 9)
	r = Replay([]Event{gc, ge, claim, created, hb, release, takeover, stale})
	requireConflict(t, r, "ol-stale", "lease/fencing token does not match latest causal ownership")
}

func TestOwnershipTakeoverMismatchAndHeartbeat(t *testing.T) {
	t.Run("mutation with pre-takeover lease", func(t *testing.T) {
		gc := withID(rawOwn("g1", "lg", "a", 1), "tm-gclaim", "d1", 1)
		ge := withID(rawCreated("g1", "goal", "", "lg", 1, "tm-gclaim"), "tm-gcreated", "d1", 2)
		claim := withID(rawOwn("n1", "l1", "agent", 1, "tm-gcreated"), "tm-claim", "d1", 3)
		created := withID(rawCreated("n1", "task", "g1", "l1", 1, "tm-claim"), "tm-created", "d1", 4)
		takeover := withID(ownType(EvOwnershipTakenOver, "n1", "l2", "other", 2, "tm-claim"), "tm-takeover", "d1", 5)
		mut := withID(status("n1", StatusInProgress, 1, "l1", 1, "tm-created", "tm-takeover"), "tm-mut", "d1", 6)
		r := Replay([]Event{gc, ge, claim, created, takeover, mut})
		requireConflict(t, r, "tm-mut", "does not match latest causal ownership")
	})
	t.Run("heartbeat keeps ownership live", func(t *testing.T) {
		gc := withID(rawOwn("g1", "lg", "a", 1), "hb-gclaim", "d1", 1)
		ge := withID(rawCreated("g1", "goal", "", "lg", 1, "hb-gclaim"), "hb-gcreated", "d1", 2)
		claim := withID(rawOwn("n1", "l1", "agent", 1, "hb-gcreated"), "hb-claim", "d1", 3)
		created := withID(rawCreated("n1", "task", "g1", "l1", 1, "hb-claim"), "hb-created", "d1", 4)
		hb := withID(ownType(EvOwnershipHeartbeat, "n1", "l1", "agent", 1, "hb-claim"), "hb-heartbeat", "d1", 5)
		mut := withID(status("n1", StatusInProgress, 1, "l1", 1, "hb-created", "hb-heartbeat"), "hb-mut", "d1", 6)
		r := Replay([]Event{gc, ge, claim, created, hb, mut})
		requireNoConflicts(t, r)
		n := r.Tree.Nodes["n1"]
		if n.Status != StatusInProgress || n.LeaseEventID != "hb-heartbeat" {
			t.Fatalf("heartbeat chain not applied: %+v", n)
		}
	})
}

func TestOwnershipFor(t *testing.T) {
	event := func(id string) Event { return Event{EventID: id} }
	isAncestorFrom := func(parents map[string]map[string]bool) func(a, b string) bool {
		return func(a, b string) bool {
			if a == b {
				return false
			}
			return parents[b][a]
		}
	}
	t.Run("no ownership events", func(t *testing.T) {
		latest, concurrent := ownershipFor(nil, "mut", isAncestorFrom(nil))
		if latest != nil || concurrent != nil {
			t.Fatalf("expected no ownership, got %+v %+v", latest, concurrent)
		}
	})
	t.Run("single causal ancestor", func(t *testing.T) {
		parents := map[string]map[string]bool{"mut": {"a": true}}
		latest, concurrent := ownershipFor([]Event{event("a")}, "mut", isAncestorFrom(parents))
		if latest == nil || latest.EventID != "a" || concurrent != nil {
			t.Fatalf("latest = %+v, concurrent = %+v", latest, concurrent)
		}
	})
	t.Run("unrelated ownership is concurrent", func(t *testing.T) {
		parents := map[string]map[string]bool{}
		latest, concurrent := ownershipFor([]Event{event("a")}, "mut", isAncestorFrom(parents))
		if latest != nil {
			t.Fatalf("unrelated ownership returned as latest: %+v", latest)
		}
		if concurrent == nil || concurrent.EventID != "a" {
			t.Fatalf("concurrent = %+v, want event a", concurrent)
		}
	})
	t.Run("causal chain returns the maximal ancestor", func(t *testing.T) {
		parents := map[string]map[string]bool{"mut": {"a": true, "b": true}, "b": {"a": true}}
		latest, concurrent := ownershipFor([]Event{event("a"), event("b")}, "mut", isAncestorFrom(parents))
		if latest == nil || latest.EventID != "b" || concurrent != nil {
			t.Fatalf("latest = %+v, concurrent = %+v", latest, concurrent)
		}
	})
	t.Run("concurrent maxima are a conflict", func(t *testing.T) {
		parents := map[string]map[string]bool{"mut": {"a": true, "b": true}}
		latest, concurrent := ownershipFor([]Event{event("a"), event("b")}, "mut", isAncestorFrom(parents))
		if latest != nil {
			t.Fatalf("concurrent maxima produced a latest: %+v", latest)
		}
		if concurrent == nil || concurrent.EventID != "a" {
			t.Fatalf("concurrent = %+v, want first maximal candidate a", concurrent)
		}
	})
}

func TestApplyMutationDirectBranches(t *testing.T) {
	lastNode := func(nodeType NodeType, status Status, revision int) *Node {
		return &Node{ID: "n1", Type: nodeType, Status: status, Revision: revision, LastEventID: "last"}
	}
	t.Run("unknown node", func(t *testing.T) {
		err := applyMutation(NewTree(), Event{Type: EvStatusChanged}, "ghost", nil)
		if err == nil || !strings.Contains(err.Error(), "node ghost not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("tombstoned node", func(t *testing.T) {
		n := lastNode(NodeTask, StatusTodo, 1)
		n.Deleted = true
		tree := &Tree{Nodes: map[string]*Node{"n1": n}}
		err := applyMutation(tree, Event{Type: EvStatusChanged, CausalParents: []string{"last"}}, "n1", nil)
		if err == nil || !strings.Contains(err.Error(), "tombstoned") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("unsupported mutation type", func(t *testing.T) {
		tree := &Tree{Nodes: map[string]*Node{"n1": lastNode(NodeTask, StatusTodo, 1)}}
		e := Event{
			Type: EvNodeCreated, CausalParents: []string{"last"},
			BaseRevision: 1, ExpectedRevision: 2,
			Payload: json.RawMessage(`{"node_id":"n1"}`),
		}
		err := applyMutation(tree, e, "n1", nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported mutation") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("self-declared attestation", func(t *testing.T) {
		tree := &Tree{Nodes: map[string]*Node{"n1": lastNode(NodeTask, StatusTodo, 1)}}
		e := Event{
			Type: EvStatusChanged, CausalParents: []string{"last"}, ActorAttestation: "local-user",
			BaseRevision: 1, ExpectedRevision: 2,
		}
		err := applyMutation(tree, e, "n1", nil)
		if err == nil || !strings.Contains(err.Error(), "actor_attestation") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("malformed payload", func(t *testing.T) {
		tree := &Tree{Nodes: map[string]*Node{"n1": lastNode(NodeTask, StatusTodo, 1)}}
		e := Event{
			Type: EvNodeUpdated, CausalParents: []string{"last"},
			BaseRevision: 1, ExpectedRevision: 2, Payload: json.RawMessage("{not-json"),
		}
		if err := applyMutation(tree, e, "n1", nil); err == nil {
			t.Fatal("malformed payload was accepted")
		}
	})
	t.Run("illegal transition", func(t *testing.T) {
		tree := &Tree{Nodes: map[string]*Node{"n1": lastNode(NodeTask, StatusTodo, 1)}}
		payload, _ := json.Marshal(NodePayload{NodeID: "n1", Status: string(StatusDone)})
		e := Event{
			Type: EvStatusChanged, CausalParents: []string{"last"},
			BaseRevision: 1, ExpectedRevision: 2, Payload: payload,
		}
		err := applyMutation(tree, e, "n1", nil)
		if err == nil || !strings.Contains(err.Error(), "illegal transition") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("status change applies", func(t *testing.T) {
		tree := &Tree{Nodes: map[string]*Node{"n1": lastNode(NodeTask, StatusTodo, 1)}}
		payload, _ := json.Marshal(NodePayload{NodeID: "n1", Status: string(StatusInProgress)})
		e := Event{
			Type: EvStatusChanged, CausalParents: []string{"last"}, EventID: "mut",
			BaseRevision: 1, ExpectedRevision: 2, Payload: payload,
		}
		if err := applyMutation(tree, e, "n1", nil); err != nil {
			t.Fatal(err)
		}
		n := tree.Nodes["n1"]
		if n.Status != StatusInProgress || n.Revision != 2 || n.LastEventID != "mut" {
			t.Fatalf("mutation not applied: %+v", n)
		}
	})
}

func evidenceNodeTree() *Tree {
	tree := NewTree()
	tree.Nodes["n1"] = &Node{ID: "n1", Type: NodeTask, Revision: 1}
	return tree
}

func evidenceEventPayload(t *testing.T, p EvidencePayload) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplyNonMutationEvidenceBranches(t *testing.T) {
	validPayload := func() EvidencePayload {
		return EvidencePayload{
			EvidenceID: "ev1", NodeID: "n1", Kind: "test",
			SHA256: strings.Repeat("a", 64), Size: 3, ArtifactSchema: 1,
		}
	}
	cases := []struct {
		name    string
		payload json.RawMessage
		verify  EvidenceVerifier
		setup   func(tree *Tree)
		want    string
	}{
		{"malformed payload", json.RawMessage("{not-json"), nil, nil, "invalid character"},
		{"missing fields", json.RawMessage(`{"evidence_id":"ev1","node_id":"n1"}`), nil, nil, "evidence_id, node_id and kind are required"},
		{"negative size", func() json.RawMessage {
			p := validPayload()
			p.Size = -1
			return evidenceEventPayload(t, p)
		}(), nil, nil, "non-negative"},
		{"negative schema", func() json.RawMessage {
			p := validPayload()
			p.ArtifactSchema = -1
			return evidenceEventPayload(t, p)
		}(), nil, nil, "invalid evidence artifact_schema"},
		{"unsupported schema", func() json.RawMessage {
			p := validPayload()
			p.ArtifactSchema = 2
			return evidenceEventPayload(t, p)
		}(), nil, nil, "unsupported evidence artifact_schema"},
		{"legacy schema with verifier", func() json.RawMessage {
			p := validPayload()
			p.ArtifactSchema = 0
			return evidenceEventPayload(t, p)
		}(), func(EvidenceEntry) error { return nil }, nil, "legacy evidence schema"},
		{"bad sha", func() json.RawMessage {
			p := validPayload()
			p.SHA256 = "1234"
			return evidenceEventPayload(t, p)
		}(), nil, nil, "64 hex"},
		{"secret metadata", func() json.RawMessage {
			p := validPayload()
			p.Kind = "password = hunter2hunter2"
			return evidenceEventPayload(t, p)
		}(), nil, nil, "metadata rejected"},
		{"unknown node", func() json.RawMessage {
			p := validPayload()
			p.NodeID = "ghost"
			return evidenceEventPayload(t, p)
		}(), nil, nil, "evidence node ghost not found"},
		{"duplicate evidence id", evidenceEventPayload(t, validPayload()), nil, func(tree *Tree) {
			tree.Evidence["ev1"] = EvidenceEntry{EvidenceID: "ev1", NodeID: "n1"}
		}, "already exists"},
		{"verification failure", evidenceEventPayload(t, validPayload()), func(EvidenceEntry) error {
			return errSentinelVerification
		}, nil, "evidence artifact verification failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := evidenceNodeTree()
			if tc.setup != nil {
				tc.setup(tree)
			}
			e := Event{Type: EvEvidenceAdded, EventID: "evt", Payload: tc.payload}
			err := applyNonMutation(tree, e, tc.verify)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	t.Run("valid evidence without verifier", func(t *testing.T) {
		tree := evidenceNodeTree()
		if err := applyNonMutation(tree, Event{Type: EvEvidenceAdded, EventID: "evt", Payload: evidenceEventPayload(t, validPayload())}, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := tree.Evidence["ev1"]; !ok {
			t.Fatal("evidence entry not projected")
		}
	})
	t.Run("valid evidence with verifier", func(t *testing.T) {
		tree := evidenceNodeTree()
		verify := func(entry EvidenceEntry) error {
			if entry.SHA256 != strings.Repeat("a", 64) || entry.Size != 3 {
				t.Fatalf("verifier entry = %+v", entry)
			}
			return nil
		}
		if err := applyNonMutation(tree, Event{Type: EvEvidenceAdded, Payload: evidenceEventPayload(t, validPayload())}, verify); err != nil {
			t.Fatal(err)
		}
	})
}

var errSentinelVerification = &verificationError{}

type verificationError struct{}

func (*verificationError) Error() string { return "sentinel verification failure" }

func TestApplyNonMutationNodeCreatedBranches(t *testing.T) {
	nodePayload := func(t *testing.T, p NodePayload) json.RawMessage {
		t.Helper()
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name  string
		event func(t *testing.T) Event
		setup func(tree *Tree)
		want  string
	}{
		{
			name: "invalid node type",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "x", Type: "epic"})}
			},
			want: "invalid node type",
		},
		{
			name: "missing node id",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{Type: "goal"})}
			},
			want: "node_id is required",
		},
		{
			name: "non-goal requires a parent",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "x", Type: "task"})}
			},
			want: "requires a parent",
		},
		{
			name: "parent not found",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "x", Type: "task", ParentID: "ghost"})}
			},
			want: "parent ghost not found",
		},
		{
			name: "node already exists",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "n1", Type: "goal"})}
			},
			want: "already exists",
		},
		{
			name: "invalid provenance",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "x", Type: "goal", Provenance: "trust_me"})}
			},
			want: "invalid provenance",
		},
		{
			name: "user_confirmed without attestation",
			event: func(t *testing.T) Event {
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "x", Type: "goal", Provenance: string(ProvenanceUserConfirmed)})}
			},
			want: "local user attestation",
		},
		{
			name: "confidence out of range",
			event: func(t *testing.T) Event {
				over := 1.5
				return Event{Type: EvNodeCreated, Payload: nodePayload(t, NodePayload{NodeID: "x", Type: "goal", Confidence: &over})}
			},
			want: "confidence must be between",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := evidenceNodeTree()
			if tc.setup != nil {
				tc.setup(tree)
			}
			err := applyNonMutation(tree, tc.event(t), nil)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	t.Run("valid goal with agent_inferred provenance", func(t *testing.T) {
		tree := evidenceNodeTree()
		confidence := 0.5
		e := Event{
			Type: EvNodeCreated, EventID: "created",
			LeaseID: "lg", FencingToken: 2,
			Payload: nodePayload(t, NodePayload{
				NodeID: "g2", Type: "goal", Title: "Goal", NorthStar: "north",
				Provenance: string(ProvenanceAgentInferred), Confidence: &confidence,
				Owner: "agent",
			}),
		}
		if err := applyNonMutation(tree, e, nil); err != nil {
			t.Fatal(err)
		}
		n := tree.Nodes["g2"]
		if n == nil || n.NorthStar != "north" || n.Provenance != ProvenanceAgentInferred {
			t.Fatalf("goal node not projected: %+v", n)
		}
		if n.Revision != 1 || n.Status != StatusInProgress || n.LastEventID != "created" {
			t.Fatalf("goal node bookkeeping wrong: %+v", n)
		}
		if n.Confidence == nil || *n.Confidence != 0.5 {
			t.Fatalf("confidence not projected: %+v", n)
		}
		if n.LeaseID != "lg" || n.FencingToken != 2 {
			t.Fatalf("lease metadata not projected: %+v", n)
		}
	})
	t.Run("self-declared attestation is rejected", func(t *testing.T) {
		tree := evidenceNodeTree()
		e := Event{
			Type: EvNodeCreated, EventID: "attested", ActorAttestation: "local-user",
			Payload: nodePayload(t, NodePayload{NodeID: "g3", Type: "goal", Title: "Goal"}),
		}
		err := applyNonMutation(tree, e, nil)
		if err == nil || !strings.Contains(err.Error(), "actor_attestation") {
			t.Fatalf("self-declared attestation was accepted: %v", err)
		}
		if _, ok := tree.Nodes["g3"]; ok {
			t.Fatal("attested event created a node")
		}
	})
	t.Run("user_confirmed is rejected even with attestation", func(t *testing.T) {
		tree := evidenceNodeTree()
		e := Event{
			Type: EvNodeCreated, EventID: "confirmed", ActorAttestation: "local-user",
			Payload: nodePayload(t, NodePayload{NodeID: "g4", Type: "goal", Title: "Goal", Provenance: string(ProvenanceUserConfirmed)}),
		}
		if err := applyNonMutation(tree, e, nil); err == nil {
			t.Fatal("user_confirmed with self-declared attestation was accepted")
		}
		e.ActorAttestation = ""
		if err := applyNonMutation(tree, e, nil); err == nil || !strings.Contains(err.Error(), "user_confirmed") {
			t.Fatalf("user_confirmed without verified attestation was accepted: %v", err)
		}
	})
}

func TestApplyNonMutationOwnershipAndUnsupported(t *testing.T) {
	tree := evidenceNodeTree()
	payload, _ := json.Marshal(OwnershipPayload{NodeID: "n1", Owner: "agent", LeaseID: "l1", FencingToken: 3})
	e := Event{Type: EvOwnershipClaimed, EventID: "own", Payload: payload}
	if err := applyNonMutation(tree, e, nil); err != nil {
		t.Fatal(err)
	}
	n := tree.Nodes["n1"]
	if n.Owner != "agent" || n.LeaseID != "l1" || n.FencingToken != 3 || n.LeaseEventID != "own" {
		t.Fatalf("ownership not projected: %+v", n)
	}
	// Ownership for a node that does not exist yet is applied as a no-op so a
	// later node_created can bind the lease.
	ghost, _ := json.Marshal(OwnershipPayload{NodeID: "ghost", Owner: "agent", LeaseID: "l2"})
	if err := applyNonMutation(tree, Event{Type: EvOwnershipClaimed, Payload: ghost}, nil); err != nil {
		t.Fatalf("ownership for an unknown node returned an error: %v", err)
	}
	if _, ok := tree.Nodes["ghost"]; ok {
		t.Fatal("ownership created a node")
	}
	if err := applyNonMutation(tree, Event{Type: EvOwnershipClaimed, Payload: json.RawMessage("{bad")}, nil); err == nil {
		t.Fatal("malformed ownership payload was accepted")
	}
	if err := applyNonMutation(tree, Event{Type: EventType("mystery")}, nil); err == nil {
		t.Fatal("unknown event type was accepted")
	} else if !strings.Contains(err.Error(), "unsupported event type") {
		t.Fatalf("unexpected error: %v", err)
	}
}
