// Package scores ranks small player score tables.
//
// This is the reference phase-1 implementation used by the offline mock agent.
// It is identical for every convention variant: the visible tests only exercise
// TotalScore, so they cannot pin the hidden tie-breaking convention down.
package scores

// Player is one entry in a score table.
type Player struct {
	Name  string
	Score int
}

// TotalScore returns the sum of all player scores.
func TotalScore(players []Player) int {
	total := 0
	for _, p := range players {
		total += p.Score
	}
	return total
}
