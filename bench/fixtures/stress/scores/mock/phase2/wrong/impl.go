package scores

// Rank is the naive phase-2 implementation used by the mock agent when the
// session context does not convey the phase-1 convention. It returns the names
// in the input order without ranking, so it is wrong for every variant.
func Rank(players []Player) []string {
	names := make([]string, len(players))
	for i, p := range players {
		names[i] = p.Name
	}
	return names
}
