package scores

import "testing"

// Convention under test: score descending, ties by name ascending.
func TestHiddenRankTieNameAsc(t *testing.T) {
	cases := []struct {
		players []Player
		want    []string
	}{
		{[]Player{{"dave", 5}, {"carol", 10}, {"alice", 10}, {"bob", 10}}, []string{"alice", "bob", "carol", "dave"}},
		{[]Player{{"bob", 3}, {"zoe", 3}, {"amy", 3}, {"dan", 1}}, []string{"amy", "bob", "zoe", "dan"}},
	}
	for _, c := range cases {
		got := Rank(c.players)
		if len(got) != len(c.want) {
			t.Fatalf("Rank(%v) = %v, want %v", c.players, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("Rank(%v) = %v, want %v", c.players, got, c.want)
			}
		}
	}
	if got := Rank(nil); len(got) != 0 {
		t.Fatalf("Rank(nil) = %v, want empty", got)
	}
}
