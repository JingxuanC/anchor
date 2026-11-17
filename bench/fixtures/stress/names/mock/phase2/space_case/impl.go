package names

import "strings"

// NormalizeName trims a display name.
// Convention: collapse every internal run of whitespace to a single space and
// preserve letter case.
func NormalizeName(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", ErrEmptyName
	}
	return strings.Join(strings.Fields(trimmed), " "), nil
}
