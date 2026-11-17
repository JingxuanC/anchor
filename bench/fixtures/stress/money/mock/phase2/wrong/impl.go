package money

import "fmt"

// Allocate is the naive phase-2 implementation used by the mock agent when the
// session context does not convey the phase-1 convention. It ignores the
// weights entirely and drops the remainder, so it is wrong for every variant.
func Allocate(totalCents int64, weights []int64) ([]int64, error) {
	if totalCents < 0 {
		return nil, fmt.Errorf("money: total %d is negative", totalCents)
	}
	if len(weights) == 0 {
		return nil, fmt.Errorf("money: at least one weight is required")
	}
	for _, w := range weights {
		if w <= 0 {
			return nil, fmt.Errorf("money: weight %d is not positive", w)
		}
	}
	parts := make([]int64, len(weights))
	each := totalCents / int64(len(weights))
	for i := range parts {
		parts[i] = each
	}
	return parts, nil
}
