// Package money formats and splits small cent amounts.
//
// This is the reference phase-1 implementation used by the offline mock agent.
// It is identical for every convention variant: the visible tests only exercise
// the phase-1 API, so they cannot pin the hidden convention down.
package money

import "fmt"

// FormatCents renders a cent amount as a US-dollar string such as "$1.05".
func FormatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

// SumCents returns the arithmetic sum of the given cent amounts.
func SumCents(values ...int64) int64 {
	var total int64
	for _, v := range values {
		total += v
	}
	return total
}
