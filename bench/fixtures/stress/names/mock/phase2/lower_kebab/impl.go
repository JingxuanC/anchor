package names

import "strings"

// NormalizeName normalizes a display name.
// Convention: trim, lowercase, and replace internal whitespace runs with one
// hyphen.
func NormalizeName(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", ErrEmptyName
	}
	return strings.Join(strings.Fields(strings.ToLower(trimmed)), "-"), nil
}
