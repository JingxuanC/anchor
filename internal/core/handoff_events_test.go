package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func handoffEvent(t *testing.T, typ EventType, p HandoffPayload, parents ...string) Event {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return Event{Type: typ, CausalParents: parents, Payload: b}
}

func TestHandoffAckAndVerifyAreProjected(t *testing.T) {
	s := initTest(t)
	mustAppend(t, s, handoffEvent(t, EvHandoffAcked, HandoffPayload{HandoffID: "ho_1", ContentHash: strings.Repeat("a", 64), Verifier: "agent-b"}))
	mustAppend(t, s, handoffEvent(t, EvHandoffVerified, HandoffPayload{HandoffID: "ho_1", ContentHash: strings.Repeat("a", 64),
		CodeHead: strings.Repeat("b", 40), EvidenceIDs: []string{}, Verifier: "agent-b", Note: "checked"}))

	replay, err := s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	st := replay.Tree.Handoffs["ho_1"]
	if st == nil {
		t.Fatal("handoff state was not projected")
	}
	if !st.Acked || !st.Verified || st.AckedBy != "agent-b" || st.VerifiedBy != "agent-b" {
		t.Fatalf("handoff progress wrong: %+v", st)
	}
	if st.CodeHead != strings.Repeat("b", 40) || st.ContentHash != strings.Repeat("a", 64) {
		t.Fatalf("handoff observation lost: %+v", st)
	}
	if st.Forced {
		t.Fatalf("unforced events marked forced: %+v", st)
	}
}

func TestHandoffVerifiedRequiresAck(t *testing.T) {
	s := initTest(t)
	_, _, err := s.Append(handoffEvent(t, EvHandoffVerified, HandoffPayload{HandoffID: "ho_1", CodeHead: strings.Repeat("b", 40)}))
	if err == nil {
		t.Fatal("verification without acknowledgement was accepted")
	}
	if !strings.Contains(err.Error(), "requires a prior handoff_acked") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandoffDuplicateAckAndVerifyRejected(t *testing.T) {
	s := initTest(t)
	mustAppend(t, s, handoffEvent(t, EvHandoffAcked, HandoffPayload{HandoffID: "ho_1"}))
	if _, _, err := s.Append(handoffEvent(t, EvHandoffAcked, HandoffPayload{HandoffID: "ho_1"})); err == nil {
		t.Fatal("duplicate ack was accepted")
	} else if !strings.Contains(err.Error(), "already acked") {
		t.Fatalf("unexpected error: %v", err)
	}
	mustAppend(t, s, handoffEvent(t, EvHandoffVerified, HandoffPayload{HandoffID: "ho_1", CodeHead: strings.Repeat("b", 40)}))
	if _, _, err := s.Append(handoffEvent(t, EvHandoffVerified, HandoffPayload{HandoffID: "ho_1"})); err == nil {
		t.Fatal("duplicate verification was accepted")
	}
	// A forced repeat is allowed and recorded.
	mustAppend(t, s, handoffEvent(t, EvHandoffAcked, HandoffPayload{HandoffID: "ho_1", Forced: true}))
	replay, err := s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Tree.Handoffs["ho_1"].Forced {
		t.Fatal("forced flag not projected")
	}
}

func TestHandoffEventValidation(t *testing.T) {
	s := initTest(t)
	cases := []struct {
		name string
		p    HandoffPayload
		want string
	}{
		{"missing id", HandoffPayload{}, "no handoff_id"},
		{"bad content hash", HandoffPayload{HandoffID: "h", ContentHash: "zz"}, "invalid content hash"},
		{"bad code head", HandoffPayload{HandoffID: "h", CodeHead: "not-a-sha"}, "invalid code head"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.Append(handoffEvent(t, EvHandoffAcked, tc.p)); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	// Verification must not name evidence the workspace does not have.
	mustAppend(t, s, handoffEvent(t, EvHandoffAcked, HandoffPayload{HandoffID: "h"}))
	if _, _, err := s.Append(handoffEvent(t, EvHandoffVerified, HandoffPayload{HandoffID: "h", EvidenceIDs: []string{"ghost"}})); err == nil {
		t.Fatal("verification of unknown evidence was accepted")
	} else if !strings.Contains(err.Error(), "unknown evidence") {
		t.Fatalf("unexpected error: %v", err)
	}
}
