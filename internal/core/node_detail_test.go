package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func nodeCreatePayload(t *testing.T, s *Store, node, typ, title, parent, lease string, token uint64, p NodePayload, parents ...string) Event {
	t.Helper()
	p.NodeID = node
	p.Type = typ
	p.Title = title
	p.ParentID = parent
	if p.Owner == "" {
		p.Owner = "agent"
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return mustAppend(t, s, Event{Type: EvNodeCreated, CausalParents: parents, LeaseID: lease, FencingToken: token, Payload: b})
}

func TestGoalDetailFieldsAreProjected(t *testing.T) {
	s := initTest(t)
	claim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	conf := 0.4
	created := nodeCreatePayload(t, s, "g1", "goal", "Ship", "", "lg", 1, NodePayload{
		NorthStar:       "successor resumes with zero context loss",
		Must:            []string{"keep evidence immutable"},
		MustNot:         []string{"no local causal database"},
		SuccessCriteria: []string{"restore works on a second device"},
		Provenance:      string(ProvenanceAgentInferred),
		Confidence:      &conf,
	}, claim.EventID)
	_ = created

	replay, err := s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := replay.Tree.Nodes["g1"]
	if !ok {
		t.Fatal("goal node missing from projection")
	}
	if n.NorthStar != "successor resumes with zero context loss" {
		t.Fatalf("north star not projected: %q", n.NorthStar)
	}
	if len(n.Must) != 1 || n.Must[0] != "keep evidence immutable" {
		t.Fatalf("must constraints not projected: %v", n.Must)
	}
	if len(n.MustNot) != 1 || n.MustNot[0] != "no local causal database" {
		t.Fatalf("must-not constraints not projected: %v", n.MustNot)
	}
	if len(n.SuccessCriteria) != 1 {
		t.Fatalf("success criteria not projected: %v", n.SuccessCriteria)
	}
	if n.Provenance != ProvenanceAgentInferred {
		t.Fatalf("provenance not projected: %q", n.Provenance)
	}
	if n.Confidence == nil || *n.Confidence != 0.4 {
		t.Fatalf("confidence not projected: %v", n.Confidence)
	}
}

func TestDecisionDetailFieldsAreProjected(t *testing.T) {
	s := initTest(t)
	gc := mustAppend(t, s, own("g1", "lg", "agent", 1))
	nodeCreatePayload(t, s, "g1", "goal", "Goal", "", "lg", 1, NodePayload{}, gc.EventID)
	dc := mustAppend(t, s, own("d1", "ld", "agent", 1, gc.EventID))
	nodeCreatePayload(t, s, "d1", "decision", "Packet hashing", "g1", "ld", 1, NodePayload{
		Decision:     "hash the packet body",
		Rationale:    "detect tampering",
		Alternatives: []string{"sign with a key"},
		Consequences: []string{"no key management"},
	}, dc.EventID)

	replay, err := s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	n := replay.Tree.Nodes["d1"]
	if n.Decision != "hash the packet body" || n.Rationale != "detect tampering" {
		t.Fatalf("decision fields not projected: %+v", n)
	}
	if len(n.Alternatives) != 1 || len(n.Consequences) != 1 {
		t.Fatalf("trade-offs not projected: %+v", n)
	}
}

func TestGoalFieldsRejectedOnNonGoalNode(t *testing.T) {
	s := initTest(t)
	gc := mustAppend(t, s, own("g1", "lg", "agent", 1))
	nodeCreatePayload(t, s, "g1", "goal", "Goal", "", "lg", 1, NodePayload{}, gc.EventID)
	tc := mustAppend(t, s, own("t1", "lt", "agent", 1, gc.EventID))
	p := NodePayload{NodeID: "t1", Type: "task", Title: "Task", ParentID: "g1", NorthStar: "sneaky"}
	b, _ := json.Marshal(p)
	if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{tc.EventID}, LeaseID: "lt", FencingToken: 1, Payload: b}); err == nil {
		t.Fatal("goal fields were accepted on a task node")
	} else if !strings.Contains(err.Error(), "goal fields require a goal node") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecisionFieldsRejectedOnNonDecisionNode(t *testing.T) {
	s := initTest(t)
	gc := mustAppend(t, s, own("g1", "lg", "agent", 1))
	nodeCreatePayload(t, s, "g1", "goal", "Goal", "", "lg", 1, NodePayload{}, gc.EventID)
	tc := mustAppend(t, s, own("t1", "lt", "agent", 1, gc.EventID))
	p := NodePayload{NodeID: "t1", Type: "task", Title: "Task", ParentID: "g1", Decision: "sneaky"}
	b, _ := json.Marshal(p)
	if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{tc.EventID}, LeaseID: "lt", FencingToken: 1, Payload: b}); err == nil {
		t.Fatal("decision fields were accepted on a task node")
	} else if !strings.Contains(err.Error(), "decision fields require a decision node") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUserConfirmedProvenanceRequiresAttestation(t *testing.T) {
	s := initTest(t)
	claim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	p := NodePayload{NodeID: "g1", Type: "goal", Title: "Goal", Provenance: string(ProvenanceUserConfirmed)}
	b, _ := json.Marshal(p)
	if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: "lg", FencingToken: 1, Payload: b}); err == nil {
		t.Fatal("agent self-reported user_confirmed provenance was accepted")
	} else if !strings.Contains(err.Error(), "user attestation") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProvenanceAndConfidenceAreValidated(t *testing.T) {
	s := initTest(t)
	claim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	bad := NodePayload{NodeID: "g1", Type: "goal", Title: "Goal", Provenance: "trust_me"}
	b, _ := json.Marshal(bad)
	if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: "lg", FencingToken: 1, Payload: b}); err == nil {
		t.Fatal("unknown provenance value was accepted")
	}
	bad.Provenance = string(ProvenanceAgentInferred)
	over := 1.5
	bad.Confidence = &over
	b, _ = json.Marshal(bad)
	if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: "lg", FencingToken: 1, Payload: b}); err == nil {
		t.Fatal("confidence above 1 was accepted")
	}
}

func TestNodeUpdateCarriesDetailFields(t *testing.T) {
	s := initTest(t)
	claim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	created := nodeCreatePayload(t, s, "g1", "goal", "Goal", "", "lg", 1, NodePayload{}, claim.EventID)
	two := 2.0
	update := NodePayload{NodeID: "g1", NorthStar: "north star v2", Must: []string{"stay local"}, Confidence: &two}
	b, _ := json.Marshal(update)
	if _, _, err := s.Append(Event{Type: EvNodeUpdated, CausalParents: []string{created.EventID}, BaseRevision: 1, ExpectedRevision: 2, LeaseID: "lg", FencingToken: 1, Payload: b}); err == nil {
		t.Fatal("confidence above 1 was accepted on update")
	}
	update.Confidence = nil
	b, _ = json.Marshal(update)
	if _, _, err := s.Append(Event{Type: EvNodeUpdated, CausalParents: []string{created.EventID}, BaseRevision: 1, ExpectedRevision: 2, LeaseID: "lg", FencingToken: 1, Payload: b}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	n := replay.Tree.Nodes["g1"]
	if n.NorthStar != "north star v2" || len(n.Must) != 1 || n.Revision != 2 {
		t.Fatalf("update did not apply detail fields: %+v", n)
	}
}

// TestSelfDeclaredAttestationCannotGrantUserConfirmation covers the bypass found
// in review: a raw event with a made-up actor_attestation must not be able to
// record user_confirmed provenance.
func TestSelfDeclaredAttestationCannotGrantUserConfirmation(t *testing.T) {
	s := initTest(t)
	claim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	p := NodePayload{NodeID: "g1", Type: "goal", Title: "Self-confirmed", Provenance: string(ProvenanceUserConfirmed)}
	b, _ := json.Marshal(p)
	_, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: "lg", FencingToken: 1, ActorAttestation: "x", Payload: b})
	if err == nil {
		t.Fatal("event with self-declared attestation was accepted")
	}
	if !strings.Contains(err.Error(), "actor_attestation") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Even with a valid-looking attestation string the provenance alone must fail.
	_, _, err = s.Append(Event{Type: EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: "lg", FencingToken: 1, Payload: b})
	if err == nil || !strings.Contains(err.Error(), "user_confirmed") {
		t.Fatalf("user_confirmed without attestation accepted: %v", err)
	}
}

func TestEmptyConstraintEntriesAreRejected(t *testing.T) {
	s := initTest(t)
	claim := mustAppend(t, s, own("g1", "lg", "agent", 1))
	for _, list := range [][]string{{""}, {"  "}, {"valid", ""}} {
		p := NodePayload{NodeID: "g1", Type: "goal", Title: "G", Must: list}
		b, _ := json.Marshal(p)
		if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: "lg", FencingToken: 1, Payload: b}); err == nil {
			t.Fatalf("empty constraint entry accepted: %q", list)
		}
	}
}
