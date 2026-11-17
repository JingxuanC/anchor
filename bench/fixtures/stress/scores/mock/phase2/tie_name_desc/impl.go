package scores

import "sort"

// Rank orders player names by score descending.
// Convention: score ties order names descending.
func Rank(players []Player) []string {
	idx := make([]int, len(players))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		pa, pb := players[idx[a]], players[idx[b]]
		if pa.Score != pb.Score {
			return pa.Score > pb.Score
		}
		return pa.Name > pb.Name
	})
	names := make([]string, len(idx))
	for i, j := range idx {
		names[i] = players[j].Name
	}
	return names
}
