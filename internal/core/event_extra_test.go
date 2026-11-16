package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventNodeID(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
		wantErr string
	}{
		{"valid", `{"node_id":"n1"}`, "n1", ""},
		{"valid with extra fields", `{"node_id":"n2","status":"todo"}`, "n2", ""},
		{"missing node id", `{"status":"todo"}`, "", "has no node_id"},
		{"null payload", `null`, "", "has no node_id"},
		{"malformed json", `{not-json`, "", "invalid character"},
		{"wrong node id type", `{"node_id":123}`, "", "cannot unmarshal number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := Event{EventID: "evt", Payload: json.RawMessage(tc.payload)}
			got, err := e.NodeID()
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("NodeID = %q, %v; want %q, nil", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("NodeID accepted %q", tc.payload)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
			if tc.wantErr == "has no node_id" && !strings.Contains(err.Error(), "evt") {
				t.Fatalf("error %q does not name the event", err)
			}
		})
	}
	if _, err := (Event{EventID: "nil", Payload: nil}).NodeID(); err == nil {
		t.Fatal("nil payload produced a node id")
	}
}

func TestEventOwnershipNodeID(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
		wantErr string
	}{
		{"valid", `{"node_id":"n1","owner":"a"}`, "n1", ""},
		{"missing node id", `{"owner":"a"}`, "", "has no node_id"},
		{"malformed json", `{not-json`, "", "invalid character"},
		{"wrong node id type", `{"node_id":true}`, "", "cannot unmarshal bool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := Event{EventID: "own-evt", Payload: json.RawMessage(tc.payload)}
			got, err := e.OwnershipNodeID()
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("OwnershipNodeID = %q, %v; want %q, nil", got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("OwnershipNodeID accepted %q", tc.payload)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
			if tc.wantErr == "has no node_id" && !strings.Contains(err.Error(), "own-evt") {
				t.Fatalf("error %q does not name the event", err)
			}
		})
	}
}

func baseHashableEvent() Event {
	return Event{
		SchemaVersion: SchemaVersion,
		EventID:       "hash-event",
		WorkspaceID:   "ws",
		DeviceID:      "dev",
		DeviceSeq:     1,
		RecordedAt:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Type:          EvOwnershipClaimed,
		Payload:       json.RawMessage(`{"node_id":"n1"}`),
	}
}

func TestEventHashIsDeterministicAndExcludesEventHash(t *testing.T) {
	e := baseHashableEvent()
	h1, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("event hash is not deterministic")
	}
	if !strings.HasPrefix(h1, "sha256:") || !validSHA256(strings.TrimPrefix(h1, "sha256:")) {
		t.Fatalf("unexpected hash shape %q", h1)
	}
	sealed := e
	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}
	if sealed.EventHash != h1 {
		t.Fatalf("Seal wrote %q, want %q", sealed.EventHash, h1)
	}
	h3, err := sealed.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if h3 != h1 {
		t.Fatalf("hash includes event_hash: %q vs %q", h3, h1)
	}
	// Any canonical field change changes the hash.
	mutations := []struct {
		name   string
		mutate func(e *Event)
	}{
		{"event id", func(e *Event) { e.EventID = "other" }},
		{"device seq", func(e *Event) { e.DeviceSeq = 2 }},
		{"recorded at", func(e *Event) { e.RecordedAt = e.RecordedAt.Add(time.Second) }},
		{"type", func(e *Event) { e.Type = EvOwnershipHeartbeat }},
		{"payload", func(e *Event) { e.Payload = json.RawMessage(`{"node_id":"n2"}`) }},
		{"causal parents", func(e *Event) { e.CausalParents = []string{"parent"} }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			changed := e
			tc.mutate(&changed)
			h, err := changed.Hash()
			if err != nil {
				t.Fatal(err)
			}
			if h == h1 {
				t.Fatalf("hash ignores %s", tc.name)
			}
		})
	}
}

func TestEventHashAndSealErrors(t *testing.T) {
	bad := Event{EventID: "bad", Payload: json.RawMessage("{not-json")}
	if _, err := bad.Hash(); err == nil {
		t.Fatal("Hash accepted an unencodable payload")
	}
	if err := bad.Seal(); err == nil {
		t.Fatal("Seal accepted an unencodable payload")
	}
	if bad.EventHash != "" {
		t.Fatalf("failed Seal wrote event_hash %q", bad.EventHash)
	}
}

func TestEventSealDetectsTampering(t *testing.T) {
	e := baseHashableEvent()
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	stored := e.EventHash
	verify := func() {
		t.Helper()
		h, err := e.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if h != stored {
			return // tamper detected
		}
		t.Fatal("tampered event still hashes to its sealed digest")
	}
	e.DeviceSeq = 2
	verify()
	e.DeviceSeq = 1
	e.Payload = json.RawMessage(`{"node_id":"tampered"}`)
	verify()
	// Resealing the tampered event produces a new digest that no longer matches.
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	if e.EventHash == stored {
		t.Fatal("reseal did not change the digest")
	}
}

func TestEventTypeClassification(t *testing.T) {
	ownership := []EventType{EvOwnershipClaimed, EvOwnershipHeartbeat, EvOwnershipReleased, EvOwnershipTakenOver}
	for _, typ := range ownership {
		if !typ.IsOwnership() {
			t.Errorf("%s not classified as ownership", typ)
		}
		if typ.IsMutation() {
			t.Errorf("%s misclassified as mutation", typ)
		}
	}
	mutations := []EventType{EvNodeUpdated, EvStatusChanged, EvNoteUpdated, EvNodeTombstoned}
	for _, typ := range mutations {
		if !typ.IsMutation() {
			t.Errorf("%s not classified as mutation", typ)
		}
		if typ.IsOwnership() {
			t.Errorf("%s misclassified as ownership", typ)
		}
	}
	for _, typ := range []EventType{EvNodeCreated, EvEvidenceAdded, EvAdapterSessionSeen, EvAdapterSessionEnded, "unknown"} {
		if typ.IsOwnership() || typ.IsMutation() {
			t.Errorf("%s unexpectedly classified", typ)
		}
	}
}
