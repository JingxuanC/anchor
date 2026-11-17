package scores

import "testing"

func TestTotalScore(t *testing.T) {
	if got := TotalScore(nil); got != 0 {
		t.Errorf("TotalScore(nil) = %d, want 0", got)
	}
	players := []Player{{"a", 10}, {"b", 5}, {"c", -3}}
	if got := TotalScore(players); got != 12 {
		t.Errorf("TotalScore(%v) = %d, want 12", players, got)
	}
}
