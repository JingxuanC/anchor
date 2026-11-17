// Package scores ranks small player score tables.
//
// It is deliberately small: the phase-1 tests below only exercise the phase-1
// API. How a later session must break score ties is decided in phase 1 and is
// not encoded anywhere in this repository.
package scores

// Player is one entry in a score table.
type Player struct {
	Name  string
	Score int
}

// TotalScore returns the sum of all player scores (zero for an empty table).
func TotalScore(players []Player) int {
	// TODO(phase 1): implement.
	return 0
}
