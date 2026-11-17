package names

import "strings"

// NormalizeName is the naive phase-2 implementation used by the mock agent when
// the session context does not convey the phase-1 convention. It only trims, so
// it is wrong for every variant.
func NormalizeName(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", ErrEmptyName
	}
	return trimmed, nil
}
