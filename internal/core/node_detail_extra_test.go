package core

import (
	"strings"
	"testing"
)

// TestDetailListEntriesMustNotBeEmpty pins the replay rule that goal and
// decision list entries must carry content (an empty --must is rejected).
func TestDetailListEntriesMustNotBeEmpty(t *testing.T) {
	cases := []struct {
		name string
		typ  NodeType
		p    NodePayload
	}{
		{"goal must", NodeGoal, NodePayload{Must: []string{" "}}},
		{"goal must not", NodeGoal, NodePayload{MustNot: []string{""}}},
		{"goal success criteria", NodeGoal, NodePayload{SuccessCriteria: []string{"\t"}}},
		{"decision alternatives", NodeDecision, NodePayload{Alternatives: []string{""}}},
		{"decision consequences", NodeDecision, NodePayload{Consequences: []string{" "}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateNodeDetailFields(tc.typ, tc.p); err == nil {
				t.Fatalf("%s accepted an empty entry", tc.name)
			} else if !strings.Contains(err.Error(), "must not be empty") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	if err := validateNodeDetailFields(NodeGoal, NodePayload{Must: []string{"local only"}}); err != nil {
		t.Fatalf("valid goal detail fields rejected: %v", err)
	}
	if err := validateNodeDetailFields(NodeDecision, NodePayload{
		Alternatives: []string{"signing"}, Consequences: []string{"no keys"},
	}); err != nil {
		t.Fatalf("valid decision detail fields rejected: %v", err)
	}
}
