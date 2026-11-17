package scores

import "sort"

// Rank orders player names by score descending.
// Convention: score ties keep the original input order (stable sort).
func Rank(players []Player) []string {
	idx := make([]int, len(players))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return players[idx[a]].Score > players[idx[b]].Score
	})
	names := make([]string, len(idx))
	for i, j := range idx {
		names[i] = players[j].Name
	}
	return names
}
