package core

import (
	"sort"
)

type NodeType string

const (
	NodeGoal     NodeType = "goal"
	NodeDecision NodeType = "decision"
	NodeTask     NodeType = "task"
	NodeBranch   NodeType = "branch"
	NodeQuestion NodeType = "question"
	NodeNote     NodeType = "note"
)

type Status string

const (
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
	StatusAbandoned  Status = "abandoned"
	StatusActive     Status = "active"
	StatusArchived   Status = "archived"
)

// Provenance records how a node's content became trusted. Only a local user
// attestation may produce ProvenanceUserConfirmed; agents cannot self-report it.
type Provenance string

const (
	ProvenanceUserConfirmed Provenance = "user_confirmed"
	ProvenanceAgentInferred Provenance = "agent_inferred"
	ProvenanceImported      Provenance = "imported"
)

// ValidProvenance reports whether p is a known provenance value or empty.
func ValidProvenance(p string) bool {
	switch Provenance(p) {
	case "", ProvenanceUserConfirmed, ProvenanceAgentInferred, ProvenanceImported:
		return true
	}
	return false
}

type Node struct {
	ID       string   `json:"id"`
	Type     NodeType `json:"type"`
	Title    string   `json:"title"`
	ParentID string   `json:"parent_id,omitempty"`
	Status   Status   `json:"status"`
	Note     string   `json:"note,omitempty"`
	// Goal fields (REQ-ST-001). Only goal nodes may carry them.
	NorthStar       string   `json:"north_star,omitempty"`
	Must            []string `json:"must,omitempty"`
	MustNot         []string `json:"must_not,omitempty"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
	// Decision fields (REQ-ST-002). Only decision nodes may carry them.
	Decision     string   `json:"decision,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	Consequences []string `json:"consequences,omitempty"`
	// Provenance (REQ-ST-005) and confidence (0..1, nil when unspecified).
	Provenance   Provenance `json:"provenance,omitempty"`
	Confidence   *float64   `json:"confidence,omitempty"`
	Revision     int        `json:"revision"`
	Owner        string     `json:"owner,omitempty"`
	LeaseID      string     `json:"lease_id,omitempty"`
	LeaseEventID string     `json:"lease_event_id,omitempty"`
	FencingToken uint64     `json:"fencing_token,omitempty"`
	LastEventID  string     `json:"last_event_id,omitempty"`
	Deleted      bool       `json:"deleted,omitempty"`
}

type EvidenceEntry struct {
	EvidenceID     string `json:"evidence_id"`
	NodeID         string `json:"node_id"`
	Kind           string `json:"kind"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ArtifactSchema int    `json:"artifact_schema"`
}

type Session struct {
	ID                     string `json:"id"`
	SessionRef             string `json:"session_ref"`
	Adapter                string `json:"adapter"`
	NodeID                 string `json:"node_id"`
	Source                 string `json:"source"`
	TrustLevel             string `json:"trust_level"`
	FirstEventID           string `json:"first_event_id"`
	LastEventID            string `json:"last_event_id"`
	LastHookEvent          string `json:"last_hook_event,omitempty"`
	LastTimestamp          string `json:"last_timestamp,omitempty"`
	LastSourceEventRef     string `json:"last_source_event_ref,omitempty"`
	LastSourceSeq          uint64 `json:"last_source_seq,omitempty"`
	HistoricalObservations int    `json:"historical_observations,omitempty"`
	Ended                  bool   `json:"ended"`
	EndEventID             string `json:"end_event_id,omitempty"`
}

type Tree struct {
	Nodes    map[string]*Node         `json:"nodes"`
	Evidence map[string]EvidenceEntry `json:"evidence,omitempty"`
	Sessions map[string]Session       `json:"sessions,omitempty"`
}

func NewTree() *Tree {
	return &Tree{Nodes: map[string]*Node{}, Evidence: map[string]EvidenceEntry{}, Sessions: map[string]Session{}}
}

func (t *Tree) SortedSessions() []Session {
	out := make([]Session, 0, len(t.Sessions))
	for _, s := range t.Sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (t *Tree) SortedNodes() []*Node {
	out := make([]*Node, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CanTransition implements the legal edges in docs/02 §4.3.
func CanTransition(t NodeType, from, to Status) bool {
	switch t {
	case NodeGoal:
		switch from {
		case StatusInProgress:
			return to == StatusBlocked || to == StatusAbandoned
		case StatusBlocked:
			return to == StatusInProgress || to == StatusAbandoned
		}
		return false
	case NodeDecision, NodeTask:
		switch from {
		case StatusTodo:
			return to == StatusInProgress || to == StatusAbandoned
		case StatusInProgress:
			return to == StatusDone || to == StatusBlocked || to == StatusAbandoned
		case StatusBlocked:
			return to == StatusInProgress || to == StatusAbandoned
		case StatusDone:
			return to == StatusInProgress // reopen
		}
		return false
	case NodeQuestion:
		switch from {
		case StatusTodo:
			return to == StatusInProgress || to == StatusAbandoned
		case StatusInProgress:
			return to == StatusDone || to == StatusBlocked || to == StatusAbandoned
		case StatusBlocked:
			return to == StatusInProgress || to == StatusAbandoned
		case StatusDone:
			return to == StatusInProgress
		}
		return false
	case NodeBranch:
		switch from {
		case StatusTodo:
			return to == StatusInProgress || to == StatusAbandoned
		case StatusInProgress:
			return to == StatusDone || to == StatusAbandoned
		}
		return false
	case NodeNote:
		// Notes have no todo/done workflow; activation starts active and may be archived.
		return from == StatusActive && to == StatusArchived
	}
	return false
}

func InitialStatus(t NodeType) Status {
	switch t {
	case NodeGoal:
		return StatusInProgress
	case NodeNote:
		return StatusActive
	default:
		return StatusTodo
	}
}

func ValidNodeType(s string) (NodeType, bool) {
	switch NodeType(s) {
	case NodeGoal, NodeDecision, NodeTask, NodeBranch, NodeQuestion, NodeNote:
		return NodeType(s), true
	}
	return "", false
}
