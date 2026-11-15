package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type Conflict struct {
	EventID string `json:"event_id"`
	NodeID  string `json:"node_id,omitempty"`
	Reason  string `json:"reason"`
	Other   string `json:"other_event_id,omitempty"`
}

type ReplayResult struct {
	Applied        []string   `json:"applied"`
	Pending        []string   `json:"pending"`
	Conflicts      []Conflict `json:"conflicts"`
	Tree           *Tree      `json:"tree"`
	RecoveryStatus string     `json:"recovery_status"`
}

func sortedIDs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// EvidenceVerifier checks that a registered evidence entry still exists,
// re-hashes to its declared digest/size, belongs to the workspace, and contains
// no rejected secret material. A nil verifier is the structural-only replay
// used by isolated unit tests; production write/read paths always pass one.
type EvidenceVerifier func(EvidenceEntry) error

func Replay(events []Event) ReplayResult { return ReplayVerified(events, nil) }

func ReplayVerified(events []Event, verify EvidenceVerifier) ReplayResult {
	return replay(events, verify)
}

func replay(events []Event, verify EvidenceVerifier) ReplayResult {
	byID := make(map[string]Event, len(events))
	for _, e := range events {
		byID[e.EventID] = e
	}
	// Ancestor lookup is iterative: a malformed causal cycle must never recurse
	// to a stack overflow. Cycle nodes are isolated below.
	ancCache := map[string]map[string]bool{}
	ancestorSet := func(id string) map[string]bool {
		if a, ok := ancCache[id]; ok {
			return a
		}
		out := map[string]bool{}
		seen := map[string]bool{id: true}
		stack := []string{id}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			e, ok := byID[cur]
			if !ok {
				continue
			}
			for _, parent := range e.CausalParents {
				if _, exists := byID[parent]; !exists {
					continue
				}
				out[parent] = true
				if !seen[parent] {
					seen[parent] = true
					stack = append(stack, parent)
				}
			}
		}
		ancCache[id] = out
		return out
	}
	isAncestor := func(a, b string) bool { return a != b && ancestorSet(b)[a] }

	// Events with missing causal parents are staged; descendants are also
	// unprocessable until the parent arrives.
	missing := map[string]bool{}
	for _, e := range events {
		for _, p := range e.CausalParents {
			if _, ok := byID[p]; !ok {
				missing[e.EventID] = true
			}
		}
	}
	// Pre-scan: same-node mutations with the same base revision that are not
	// causally ordered isolate both; no first-event-wins.
	conflictReason := map[string]string{}
	conflictOther := map[string]string{}
	// Kahn pass over events whose parents exist: anything left is in (or behind)
	// a causal cycle and is isolated; missing-parent events stay pending instead.
	cycleNodes := map[string]bool{}
	{
		indeg := map[string]int{}
		children := map[string][]string{}
		for _, e := range events {
			if missing[e.EventID] {
				continue
			}
			for _, p := range e.CausalParents {
				if _, ok := byID[p]; ok {
					indeg[e.EventID]++
					children[p] = append(children[p], e.EventID)
				}
			}
		}
		q := []string{}
		for _, e := range events {
			if !missing[e.EventID] && indeg[e.EventID] == 0 {
				q = append(q, e.EventID)
			}
		}
		seen := map[string]bool{}
		for len(q) > 0 {
			id := q[0]
			q = q[1:]
			seen[id] = true
			for _, c := range children[id] {
				indeg[c]--
				if indeg[c] == 0 {
					q = append(q, c)
				}
			}
		}
		for _, e := range events {
			if !missing[e.EventID] && !seen[e.EventID] {
				cycleNodes[e.EventID] = true
			}
		}
	}
	mutationsByNode := map[string][]Event{}
	ownershipByNode := map[string][]Event{}
	for _, e := range events {
		if e.Type.IsMutation() {
			if id, err := e.NodeID(); err == nil {
				mutationsByNode[id] = append(mutationsByNode[id], e)
			}
		}
		if e.Type.IsOwnership() {
			if id, err := e.OwnershipNodeID(); err == nil {
				ownershipByNode[id] = append(ownershipByNode[id], e)
			}
		}
	}
	// Concurrent ownership changes on the same node are a conflict. Do not let
	// a later event overwrite the owner merely because it descends from the
	// same base claim.
	for _, list := range ownershipByNode {
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				a, b := list[i], list[j]
				if isAncestor(a.EventID, b.EventID) || isAncestor(b.EventID, a.EventID) {
					continue
				}
				conflictReason[a.EventID] = "concurrent ownership changes isolated"
				conflictOther[a.EventID] = b.EventID
				conflictReason[b.EventID] = "concurrent ownership changes isolated"
				conflictOther[b.EventID] = a.EventID
			}
		}
	}
	for node, list := range mutationsByNode {
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				a, b := list[i], list[j]
				if a.BaseRevision != b.BaseRevision {
					continue
				}
				if isAncestor(a.EventID, b.EventID) || isAncestor(b.EventID, a.EventID) {
					continue
				}
				conflictReason[a.EventID] = "concurrent same-revision mutations isolated"
				conflictOther[a.EventID] = b.EventID
				conflictReason[b.EventID] = "concurrent same-revision mutations isolated"
				conflictOther[b.EventID] = a.EventID
			}
		}
		_ = node
	}
	// Whole-mutation isolation: any mutation concurrent with an ownership
	// event for the same node is a conflict, even if its lease looked valid.
	for node, muts := range mutationsByNode {
		for _, m := range muts {
			if _, already := conflictReason[m.EventID]; already {
				continue
			}
			for _, o := range ownershipByNode[node] {
				if o.EventID == m.EventID {
					continue
				}
				if !isAncestor(o.EventID, m.EventID) && !isAncestor(m.EventID, o.EventID) {
					conflictReason[m.EventID] = "mutation concurrent with ownership change isolated as a whole"
					conflictOther[m.EventID] = o.EventID
					break
				}
			}
		}
	}

	tree := NewTree()
	latestOwnership := map[string]string{}
	applied := map[string]bool{}
	pending := map[string]bool{}
	conflicts := []Conflict{}
	processed := map[string]bool{}
	indegree := map[string]int{}
	children := map[string][]string{}
	for _, e := range events {
		if missing[e.EventID] {
			pending[e.EventID] = true
			continue
		}
		for _, p := range e.CausalParents {
			if _, ok := byID[p]; ok {
				indegree[e.EventID]++
				children[p] = append(children[p], e.EventID)
			}
		}
	}
	ready := []string{}
	for _, e := range events {
		if !pending[e.EventID] && indegree[e.EventID] == 0 {
			ready = append(ready, e.EventID)
		}
	}
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		if processed[id] {
			continue
		}
		e := byID[id]
		processed[id] = true
		if reason, ok := conflictReason[id]; ok {
			nid, _ := e.NodeID()
			conflicts = append(conflicts, Conflict{EventID: id, NodeID: nid, Reason: reason, Other: conflictOther[id]})
		} else if e.Type.IsMutation() {
			nodeID, err := e.NodeID()
			if err != nil {
				conflicts = append(conflicts, Conflict{EventID: id, Reason: err.Error()})
			} else {
				latest, concurrent := ownershipFor(ownershipByNode[nodeID], id, isAncestor)
				if concurrent != nil {
					conflicts = append(conflicts, Conflict{EventID: id, NodeID: nodeID, Reason: "mutation concurrent with ownership change isolated as a whole", Other: concurrent.EventID})
				} else if latest == nil {
					// The ownership parent has not arrived (or was never linked):
					// stage the mutation rather than guessing.
					pending[id] = true
					processed[id] = false
				} else if e.LeaseID != latest.LeaseID || e.FencingToken != latest.FencingToken {
					conflicts = append(conflicts, Conflict{EventID: id, NodeID: nodeID, Reason: "lease/fencing token does not match latest causal ownership", Other: latest.EventID})
				} else if err := applyMutation(tree, e, nodeID, verify); err != nil {
					conflicts = append(conflicts, Conflict{EventID: id, NodeID: nodeID, Reason: err.Error()})
				} else {
					applied[id] = true
				}
			}
		} else {
			if err := applyNonMutation(tree, e, verify); err != nil {
				conflicts = append(conflicts, Conflict{EventID: id, Reason: err.Error()})
			} else {
				applied[id] = true
				if e.Type.IsOwnership() {
					if nid, err := e.OwnershipNodeID(); err == nil {
						latestOwnership[nid] = e.EventID
						if n, ok := tree.Nodes[nid]; ok {
							n.LeaseEventID = e.EventID
						}
					}
				}
				if e.Type == EvNodeCreated {
					if nid, err := e.NodeID(); err == nil {
						if leaseEvent, ok := latestOwnership[nid]; ok {
							tree.Nodes[nid].LeaseEventID = leaseEvent
						}
					}
				}
			}
		}
		if processed[id] {
			for _, child := range children[id] {
				indegree[child]--
				// A child may have several causal parents. It becomes ready only
				// after every parent in this graph has been processed; otherwise
				// event_id sorting could evaluate it before its explicit chain.
				if !pending[child] && indegree[child] == 0 {
					ready = append(ready, child)
				}
			}
		}
	}
	// Anything not processed and not a conflict is pending (missing parent or
	// missing ownership causal parent); descendants remain unprocessed too.
	for _, e := range events {
		if !processed[e.EventID] && !applied[e.EventID] {
			pending[e.EventID] = true
		}
	}
	for id := range cycleNodes {
		nid := ""
		if e, ok := byID[id]; ok {
			nid, _ = e.NodeID()
		}
		conflicts = append(conflicts, Conflict{EventID: id, NodeID: nid, Reason: "causal cycle isolated"})
		delete(pending, id)
	}
	pendingIDs := sortedIDs(pending)
	appliedIDs := make([]string, 0, len(applied))
	for id := range applied {
		appliedIDs = append(appliedIDs, id)
	}
	sort.Strings(appliedIDs)
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].EventID == conflicts[j].EventID {
			return conflicts[i].Reason < conflicts[j].Reason
		}
		return conflicts[i].EventID < conflicts[j].EventID
	})
	status := "complete"
	if len(pendingIDs) > 0 || len(conflicts) > 0 {
		status = "incomplete"
	}
	return ReplayResult{Applied: appliedIDs, Pending: pendingIDs, Conflicts: conflicts, Tree: tree, RecoveryStatus: status}
}

func ownershipFor(all []Event, mutationID string, isAncestor func(a, b string) bool) (*Event, *Event) {
	var candidates []*Event
	for i := range all {
		o := &all[i]
		if isAncestor(o.EventID, mutationID) {
			candidates = append(candidates, o)
		} else if !isAncestor(mutationID, o.EventID) {
			return nil, o
		}
	}
	// The latest ownership is the causal maximal element among ancestors, not
	// the one with the highest device_seq (device counters are per-device).
	var maxima []*Event
	for i := range candidates {
		maximal := true
		for j := range candidates {
			if i == j {
				continue
			}
			if isAncestor(candidates[i].EventID, candidates[j].EventID) {
				maximal = false
				break
			}
		}
		if maximal {
			maxima = append(maxima, candidates[i])
		}
	}
	if len(maxima) == 1 {
		return maxima[0], nil
	}
	if len(maxima) > 1 {
		return nil, maxima[0]
	} // concurrent maxima are a conflict
	return nil, nil
}

func applyNonMutation(tree *Tree, e Event, verify EvidenceVerifier) error {
	if e.Type == EvEvidenceAdded {
		var p EvidencePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if p.EvidenceID == "" || p.NodeID == "" || p.Kind == "" {
			return fmt.Errorf("evidence_id, node_id and kind are required")
		}
		if p.Size < 0 {
			return fmt.Errorf("evidence size must be non-negative")
		}
		if p.ArtifactSchema < 0 {
			return fmt.Errorf("invalid evidence artifact_schema %d", p.ArtifactSchema)
		}
		if p.ArtifactSchema > 1 {
			return fmt.Errorf("unsupported evidence artifact_schema %d", p.ArtifactSchema)
		}
		if verify != nil && p.ArtifactSchema == 0 {
			return fmt.Errorf("legacy evidence schema: artifact bytes were not registered; explicit evidence add --file is required (no automatic migration)")
		}
		b, err := hex.DecodeString(p.SHA256)
		if err != nil || len(b) != 32 {
			return fmt.Errorf("evidence sha256 must be 64 hex characters")
		}
		if err := scanEvidenceMetadata(p.EvidenceID, p.NodeID, p.Kind); err != nil {
			return err
		}
		if _, ok := tree.Nodes[p.NodeID]; !ok {
			return fmt.Errorf("evidence node %s not found", p.NodeID)
		}
		if _, exists := tree.Evidence[p.EvidenceID]; exists {
			return fmt.Errorf("evidence %s already exists", p.EvidenceID)
		}
		entry := EvidenceEntry{EvidenceID: p.EvidenceID, NodeID: p.NodeID, Kind: p.Kind, SHA256: p.SHA256, Size: p.Size, ArtifactSchema: p.ArtifactSchema}
		if verify != nil {
			if err := verify(entry); err != nil {
				return fmt.Errorf("evidence artifact verification failed: %w", err)
			}
		}
		tree.Evidence[p.EvidenceID] = entry
		return nil
	}
	if e.Type == EvAdapterSessionSeen || e.Type == EvAdapterSessionEnded {
		var p AdapterSessionPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		ending := e.Type == EvAdapterSessionEnded
		if err := validateAdapterSessionPayload(tree, e, p, ending); err != nil {
			return err
		}
		sess := tree.Sessions[p.SessionID]
		if sess.ID == "" {
			sess = Session{ID: p.SessionID, SessionRef: p.SessionRef, Adapter: p.Adapter, NodeID: p.NodeID, Source: p.Source, TrustLevel: p.TrustLevel, FirstEventID: e.EventID}
		}
		if ending {
			if sess.LastSourceSeq > 0 && p.SourceSeq == 0 {
				return fmt.Errorf("session_end requires source_seq after sequenced observations")
			}
			if sess.LastSourceSeq > 0 && p.SourceSeq <= sess.LastSourceSeq {
				return fmt.Errorf("session_end source_seq %d is not newer than latest observed %d", p.SourceSeq, sess.LastSourceSeq)
			}
			sess.Ended = true
			sess.EndEventID = e.EventID
			sess.LastEventID = e.EventID
			sess.LastHookEvent = p.HookEvent
			sess.LastTimestamp = p.Timestamp
			if p.SourceSeq > 0 {
				sess.LastSourceSeq = p.SourceSeq
				sess.LastSourceEventRef = p.SourceEventRef
			}
			tree.Sessions[p.SessionID] = sess
			return nil
		}
		if sess.Ended {
			// A closed session never reopens. A lower source_seq is immutable
			// history and cannot move last_*; an unsequenced or newer event is a
			// late observation that must be rejected.
			if p.SourceSeq == 0 {
				return fmt.Errorf("late observation after session_end has no source_seq; rejected")
			}
			if sess.LastSourceSeq > 0 && p.SourceSeq < sess.LastSourceSeq {
				sess.HistoricalObservations++
				tree.Sessions[p.SessionID] = sess
				return nil
			}
			if sess.LastSourceSeq > 0 && p.SourceSeq == sess.LastSourceSeq {
				return fmt.Errorf("source_seq %d collides with latest observation and cannot be ordered", p.SourceSeq)
			}
			return fmt.Errorf("late observation after session_end (source_seq %d > %d); rejected", p.SourceSeq, sess.LastSourceSeq)
		}
		if p.SourceSeq > 0 && sess.LastSourceSeq > 0 {
			if p.SourceSeq < sess.LastSourceSeq {
				sess.HistoricalObservations++
				tree.Sessions[p.SessionID] = sess
				return nil
			}
			if p.SourceSeq == sess.LastSourceSeq {
				return fmt.Errorf("source_seq %d collides with latest observation and cannot be ordered", p.SourceSeq)
			}
		}
		if p.SourceSeq == 0 && sess.LastSourceSeq > 0 {
			return fmt.Errorf("sequenced session requires source_seq for new observations")
		}
		sess.LastEventID = e.EventID
		sess.LastHookEvent = p.HookEvent
		sess.LastTimestamp = p.Timestamp
		if p.SourceSeq > 0 {
			sess.LastSourceSeq = p.SourceSeq
			sess.LastSourceEventRef = p.SourceEventRef
		}
		tree.Sessions[p.SessionID] = sess
		return nil
	}
	if e.Type.IsOwnership() {
		id, err := e.OwnershipNodeID()
		if err != nil {
			return err
		}
		var p OwnershipPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if n, ok := tree.Nodes[id]; ok {
			n.Owner = p.Owner
			n.LeaseID = p.LeaseID
			n.FencingToken = p.FencingToken
			n.LeaseEventID = e.EventID
		}
		return nil
	}
	if e.Type == EvNodeCreated {
		var p NodePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		t, ok := ValidNodeType(p.Type)
		if !ok {
			return fmt.Errorf("invalid node type %q", p.Type)
		}
		if p.NodeID == "" {
			return fmt.Errorf("node_id is required")
		}
		if t != NodeGoal && p.ParentID == "" {
			return fmt.Errorf("non-goal node %s requires a parent", p.NodeID)
		}
		if _, exists := tree.Nodes[p.NodeID]; exists {
			return fmt.Errorf("node %s already exists", p.NodeID)
		}
		if p.ParentID != "" {
			if _, ok := tree.Nodes[p.ParentID]; !ok {
				return fmt.Errorf("parent %s not found", p.ParentID)
			}
		}
		n := &Node{ID: p.NodeID, Type: t, Title: p.Title, ParentID: p.ParentID, Status: InitialStatus(t), Revision: 1, Owner: p.Owner, LeaseID: e.LeaseID, FencingToken: e.FencingToken, LastEventID: e.EventID}
		tree.Nodes[p.NodeID] = n
		return nil
	}
	return fmt.Errorf("unsupported event type %s", e.Type)
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func applyMutation(tree *Tree, e Event, nodeID string, verify EvidenceVerifier) error {
	n, ok := tree.Nodes[nodeID]
	if !ok {
		return fmt.Errorf("node %s not found", nodeID)
	}
	if n.Deleted {
		return fmt.Errorf("node %s is tombstoned and cannot be mutated", nodeID)
	}
	// Mutations must explicitly name the latest node event as a causal parent.
	// Sorting by event_id/device_seq is never enough for a trusted write.
	if n.LastEventID != "" && !containsString(e.CausalParents, n.LastEventID) {
		return fmt.Errorf("mutation must name latest node event %s as a causal parent", n.LastEventID)
	}
	if e.BaseRevision != n.Revision {
		return fmt.Errorf("revision conflict: event base %d, node revision %d", e.BaseRevision, n.Revision)
	}
	// expected_revision=0 is not a wildcard. Every mutation must state and match
	// the exact next revision; otherwise an old writer can silently apply.
	if e.ExpectedRevision != n.Revision+1 {
		return fmt.Errorf("expected_revision conflict: event expects %d, next revision is %d", e.ExpectedRevision, n.Revision+1)
	}
	var p NodePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	switch e.Type {
	case EvStatusChanged:
		to := Status(p.Status)
		if !CanTransition(n.Type, n.Status, to) {
			return fmt.Errorf("illegal transition %s: %s -> %s", n.Type, n.Status, to)
		}
		if to == StatusDone {
			// The gate proves referenced artifact bytes are present, bound to
			// this node, and unchanged. It is not a causal proof that the goal
			// was actually achieved.
			if p.UserOverride {
				return fmt.Errorf("user_override is disabled: no trusted local attestation mechanism is available")
			}
			if len(p.EvidenceIDs) == 0 {
				return fmt.Errorf("done requires evidence_ids")
			}
			for _, evidenceID := range p.EvidenceIDs {
				entry, ok := tree.Evidence[evidenceID]
				if !ok {
					return fmt.Errorf("done references unknown evidence %q", evidenceID)
				}
				if entry.NodeID != nodeID {
					return fmt.Errorf("evidence %q belongs to node %s, not %s", evidenceID, entry.NodeID, nodeID)
				}
				if verify != nil {
					if err := verify(entry); err != nil {
						return fmt.Errorf("evidence %q artifact verification failed: %w", evidenceID, err)
					}
				}
			}
		}
		n.Status = to
	case EvNodeUpdated:
		if p.Title != "" {
			n.Title = p.Title
		}
	case EvNoteUpdated:
		if n.Type != NodeNote {
			return fmt.Errorf("note_updated requires a note node")
		}
		n.Note = p.Note
	case EvNodeTombstoned:
		n.Deleted = true
	default:
		return fmt.Errorf("unsupported mutation %s", e.Type)
	}
	n.Revision++
	n.LastEventID = e.EventID
	return nil
}
