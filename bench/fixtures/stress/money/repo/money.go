// Package money formats and splits small cent amounts.
//
// It is deliberately small: the phase-1 tests below only exercise the phase-1
// API. The allocation convention a later session must follow is decided in
// phase 1 and is not encoded anywhere in this repository.
package money

// FormatCents renders a cent amount as a US-dollar string such as "$1.05".
// Negative amounts place the sign before the dollar sign, e.g. -250 -> "-$2.50".
func FormatCents(cents int64) string {
	// TODO(phase 1): implement.
	return ""
}

// SumCents returns the arithmetic sum of the given cent amounts.
func SumCents(values ...int64) int64 {
	// TODO(phase 1): implement.
	return 0
}
