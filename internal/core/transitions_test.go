package core

import "testing"

func TestLegalAndIllegalTransitions(t *testing.T) {
	tests := []struct {
		typ  NodeType
		from Status
		to   Status
		ok   bool
	}{
		{NodeGoal, StatusInProgress, StatusBlocked, true},
		{NodeGoal, StatusInProgress, StatusDone, false},
		{NodeGoal, StatusTodo, StatusInProgress, false},
		{NodeTask, StatusTodo, StatusInProgress, true},
		{NodeTask, StatusTodo, StatusDone, false},
		{NodeTask, StatusInProgress, StatusDone, true},
		{NodeTask, StatusDone, StatusInProgress, true},
		{NodeTask, StatusAbandoned, StatusInProgress, false},
		{NodeDecision, StatusBlocked, StatusAbandoned, true},
		{NodeBranch, StatusInProgress, StatusDone, true},
		{NodeBranch, StatusTodo, StatusDone, false},
		{NodeNote, StatusActive, StatusArchived, true},
		{NodeNote, StatusArchived, StatusActive, false},
		{NodeNote, StatusTodo, StatusInProgress, false},
	}
	for _, tc := range tests {
		if got := CanTransition(tc.typ, tc.from, tc.to); got != tc.ok {
			t.Errorf("%s %s->%s: got %v want %v", tc.typ, tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestInitialStatus(t *testing.T) {
	if InitialStatus(NodeGoal) != StatusInProgress {
		t.Fatal("goal must start in_progress")
	}
	if InitialStatus(NodeNote) != StatusActive {
		t.Fatal("note must start active")
	}
	if InitialStatus(NodeTask) != StatusTodo {
		t.Fatal("task must start todo")
	}
}
