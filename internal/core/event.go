package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const SchemaVersion = 1

type EventType string

const (
	EvOwnershipClaimed    EventType = "ownership_claimed"
	EvOwnershipHeartbeat  EventType = "ownership_heartbeat"
	EvOwnershipReleased   EventType = "ownership_released"
	EvOwnershipTakenOver  EventType = "ownership_taken_over"
	EvNodeCreated         EventType = "node_created"
	EvNodeUpdated         EventType = "node_updated"
	EvStatusChanged       EventType = "status_changed"
	EvNoteUpdated         EventType = "note_updated"
	EvNodeTombstoned      EventType = "node_tombstoned"
	EvEvidenceAdded       EventType = "evidence_added"
	EvAdapterSessionSeen  EventType = "adapter_session_seen"
	EvAdapterSessionEnded EventType = "adapter_session_ended"
	EvHandoffAcked        EventType = "handoff_acked"
	EvHandoffVerified     EventType = "handoff_verified"
)

var ownershipTypes = map[EventType]bool{
	EvOwnershipClaimed: true, EvOwnershipHeartbeat: true,
	EvOwnershipReleased: true, EvOwnershipTakenOver: true,
}

func (t EventType) IsOwnership() bool { return ownershipTypes[t] }
func (t EventType) IsMutation() bool {
	return t == EvNodeUpdated || t == EvStatusChanged || t == EvNoteUpdated || t == EvNodeTombstoned
}

type Event struct {
	SchemaVersion    int             `json:"schema_version"`
	EventID          string          `json:"event_id"`
	WorkspaceID      string          `json:"workspace_id"`
	DeviceID         string          `json:"device_id"`
	DeviceSeq        uint64          `json:"device_seq"`
	RecordedAt       time.Time       `json:"recorded_at"`
	Type             EventType       `json:"type"`
	IdempotencyKey   string          `json:"idempotency_key"`
	CausalParents    []string        `json:"causal_parents"`
	BaseRevision     int             `json:"base_revision"`
	ExpectedRevision int             `json:"expected_revision"`
	LeaseID          string          `json:"lease_id,omitempty"`
	FencingToken     uint64          `json:"fencing_token,omitempty"`
	Payload          json.RawMessage `json:"payload"`
	EventHash        string          `json:"event_hash,omitempty"`
	ActorKind        string          `json:"actor_kind,omitempty"`
	ActorAttestation string          `json:"actor_attestation,omitempty"`
}

type NodePayload struct {
	NodeID       string   `json:"node_id"`
	Type         string   `json:"type,omitempty"`
	Title        string   `json:"title,omitempty"`
	ParentID     string   `json:"parent_id,omitempty"`
	Status       string   `json:"status,omitempty"`
	Note         string   `json:"note,omitempty"`
	Owner        string   `json:"owner,omitempty"`
	EvidenceIDs  []string `json:"evidence_ids,omitempty"`
	UserOverride bool     `json:"user_override,omitempty"`
	// Goal fields (REQ-ST-001); valid only on goal nodes.
	NorthStar       string   `json:"north_star,omitempty"`
	Must            []string `json:"must,omitempty"`
	MustNot         []string `json:"must_not,omitempty"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
	// Decision fields (REQ-ST-002); valid only on decision nodes.
	Decision     string   `json:"decision,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	Consequences []string `json:"consequences,omitempty"`
	// Provenance (REQ-ST-005).
	Provenance string   `json:"provenance,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type EvidencePayload struct {
	EvidenceID     string `json:"evidence_id"`
	NodeID         string `json:"node_id"`
	Kind           string `json:"kind"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ArtifactSchema int    `json:"artifact_schema"`
}

type AdapterSessionPayload struct {
	SessionID      string `json:"session_id"`
	SessionRef     string `json:"session_ref"`
	NodeID         string `json:"node_id"`
	Adapter        string `json:"adapter"`
	HookEvent      string `json:"hook_event"`
	Tool           string `json:"tool,omitempty"`
	CwdRef         string `json:"cwd_ref,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
	Source         string `json:"source"`
	TrustLevel     string `json:"trust_level"`
	SourceEventRef string `json:"source_event_ref"`
	SourceSeq      uint64 `json:"source_seq,omitempty"`
}

// HandoffPayload records a successor accepting (acked) or verifying a prepared
// handoff packet. It never carries code bytes; it records what the successor
// observed so the resume is auditable.
type HandoffPayload struct {
	HandoffID   string   `json:"handoff_id"`
	ContentHash string   `json:"content_hash,omitempty"`
	CodeHead    string   `json:"code_head,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Verifier    string   `json:"verifier,omitempty"`
	Forced      bool     `json:"forced,omitempty"`
	Note        string   `json:"note,omitempty"`
}

type OwnershipPayload struct {
	NodeID       string    `json:"node_id"`
	Owner        string    `json:"owner"`
	LeaseID      string    `json:"lease_id"`
	FencingToken uint64    `json:"fencing_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (e Event) NodeID() (string, error) {
	var p NodePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return "", err
	}
	if p.NodeID == "" {
		return "", fmt.Errorf("event %s has no node_id", e.EventID)
	}
	return p.NodeID, nil
}

func (e Event) OwnershipNodeID() (string, error) {
	var p OwnershipPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return "", err
	}
	if p.NodeID == "" {
		return "", fmt.Errorf("ownership event %s has no node_id", e.EventID)
	}
	return p.NodeID, nil
}

// Hash is SHA-256 over the canonical JSON body with EventHash blanked.
func (e Event) Hash() (string, error) {
	copy := e
	copy.EventHash = ""
	b, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (e *Event) Seal() error {
	h, err := e.Hash()
	if err != nil {
		return err
	}
	e.EventHash = h
	return nil
}
