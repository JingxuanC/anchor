package core

import "testing"

func TestSortedNodesDeterministicOrdering(t *testing.T) {
	tree := NewTree()
	for _, id := range []string{"n3", "n1", "n2"} {
		tree.Nodes[id] = &Node{ID: id, Title: id}
	}
	first := tree.SortedNodes()
	if len(first) != 3 {
		t.Fatalf("SortedNodes returned %d nodes", len(first))
	}
	want := []string{"n1", "n2", "n3"}
	for i, n := range first {
		if n.ID != want[i] {
			t.Fatalf("SortedNodes[%d] = %q, want %q", i, n.ID, want[i])
		}
		if n != tree.Nodes[n.ID] {
			t.Fatalf("SortedNodes[%d] is not the live node for %q", i, n.ID)
		}
	}
	for i := 0; i < 5; i++ {
		again := tree.SortedNodes()
		for j := range again {
			if again[j].ID != first[j].ID {
				t.Fatalf("SortedNodes is not deterministic: %v vs %v", again, first)
			}
		}
	}
	empty := NewTree().SortedNodes()
	if len(empty) != 0 {
		t.Fatalf("empty tree returned nodes: %v", empty)
	}
}

func TestSortedSessionsDeterministicAndValueCopied(t *testing.T) {
	tree := NewTree()
	tree.Sessions["s3"] = Session{ID: "s3", NodeID: "n3"}
	tree.Sessions["s1"] = Session{ID: "s1", NodeID: "n1"}
	tree.Sessions["s2"] = Session{ID: "s2", NodeID: "n2"}
	first := tree.SortedSessions()
	if len(first) != 3 {
		t.Fatalf("SortedSessions returned %d sessions", len(first))
	}
	for i, want := range []string{"s1", "s2", "s3"} {
		if first[i].ID != want {
			t.Fatalf("SortedSessions[%d] = %q, want %q", i, first[i].ID, want)
		}
	}
	for i := 0; i < 5; i++ {
		again := tree.SortedSessions()
		for j := range again {
			if again[j].ID != first[j].ID {
				t.Fatalf("SortedSessions is not deterministic: %v vs %v", again, first)
			}
		}
	}
	// Session values are copied out of the map; mutating the slice must not
	// corrupt the projection.
	first[0].Ended = true
	first[0].NodeID = "mutated"
	stored := tree.Sessions["s1"]
	if stored.Ended || stored.NodeID != "n1" {
		t.Fatalf("SortedSessions aliases the session map: %+v", stored)
	}
	if got := NewTree().SortedSessions(); len(got) != 0 {
		t.Fatalf("empty tree returned sessions: %v", got)
	}
}

func TestCanTransitionFullMatrix(t *testing.T) {
	statuses := []Status{
		StatusTodo, StatusInProgress, StatusDone, StatusBlocked, StatusAbandoned,
		StatusActive, StatusArchived,
	}
	legal := map[NodeType]map[Status][]Status{
		NodeGoal: {
			StatusInProgress: {StatusBlocked, StatusAbandoned},
			StatusBlocked:    {StatusInProgress, StatusAbandoned},
		},
		NodeDecision: {
			StatusTodo:       {StatusInProgress, StatusAbandoned},
			StatusInProgress: {StatusDone, StatusBlocked, StatusAbandoned},
			StatusBlocked:    {StatusInProgress, StatusAbandoned},
			StatusDone:       {StatusInProgress},
		},
		NodeTask: {
			StatusTodo:       {StatusInProgress, StatusAbandoned},
			StatusInProgress: {StatusDone, StatusBlocked, StatusAbandoned},
			StatusBlocked:    {StatusInProgress, StatusAbandoned},
			StatusDone:       {StatusInProgress},
		},
		NodeQuestion: {
			StatusTodo:       {StatusInProgress, StatusAbandoned},
			StatusInProgress: {StatusDone, StatusBlocked, StatusAbandoned},
			StatusBlocked:    {StatusInProgress, StatusAbandoned},
			StatusDone:       {StatusInProgress},
		},
		NodeBranch: {
			StatusTodo:       {StatusInProgress, StatusAbandoned},
			StatusInProgress: {StatusDone, StatusAbandoned},
		},
		NodeNote: {
			StatusActive: {StatusArchived},
		},
	}
	types := []NodeType{NodeGoal, NodeDecision, NodeTask, NodeBranch, NodeQuestion, NodeNote}
	for _, typ := range types {
		for _, from := range statuses {
			for _, to := range statuses {
				want := false
				for _, allowed := range legal[typ][from] {
					if allowed == to {
						want = true
					}
				}
				if got := CanTransition(typ, from, to); got != want {
					t.Errorf("CanTransition(%s, %s, %s) = %v, want %v", typ, from, to, got, want)
				}
			}
		}
	}
	// Unknown node types and statuses never transition.
	for _, from := range statuses {
		for _, to := range statuses {
			if CanTransition(NodeType("epic"), from, to) {
				t.Errorf("unknown node type transitioned %s -> %s", from, to)
			}
			if CanTransition(NodeTask, from, Status("paused")) {
				t.Errorf("task transitioned %s -> unknown status", from)
			}
		}
	}
	// Documented terminal properties: goal never completes, notes only archive,
	// branches never block.
	for _, from := range statuses {
		if CanTransition(NodeGoal, from, StatusDone) {
			t.Errorf("goal may transition from %s to done", from)
		}
		if from != StatusActive && CanTransition(NodeNote, from, StatusArchived) {
			t.Errorf("note may archive from %s", from)
		}
		if CanTransition(NodeBranch, from, StatusBlocked) {
			t.Errorf("branch may block from %s", from)
		}
	}
}

func TestValidNodeType(t *testing.T) {
	valid := []NodeType{NodeGoal, NodeDecision, NodeTask, NodeBranch, NodeQuestion, NodeNote}
	for _, typ := range valid {
		got, ok := ValidNodeType(string(typ))
		if !ok || got != typ {
			t.Errorf("ValidNodeType(%q) = %q, %v", typ, got, ok)
		}
	}
	invalid := []string{"", "Goal", "TASK", " goal", "goal ", "epic", "branch ", "note\n", "task?"}
	for _, s := range invalid {
		if got, ok := ValidNodeType(s); ok || got != "" {
			t.Errorf("ValidNodeType(%q) = %q, %v; want zero value and false", s, got, ok)
		}
	}
}
