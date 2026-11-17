package money

import "fmt"

// Allocate splits totalCents proportionally to weights.
// Convention: leftover cents go to the earliest parts in index order.
func Allocate(totalCents int64, weights []int64) ([]int64, error) {
	if totalCents < 0 {
		return nil, fmt.Errorf("money: total %d is negative", totalCents)
	}
	if len(weights) == 0 {
		return nil, fmt.Errorf("money: at least one weight is required")
	}
	var sum int64
	for _, w := range weights {
		if w <= 0 {
			return nil, fmt.Errorf("money: weight %d is not positive", w)
		}
		sum += w
	}
	parts := make([]int64, len(weights))
	var allocated int64
	for i, w := range weights {
		parts[i] = totalCents * w / sum
		allocated += parts[i]
	}
	leftover := totalCents - allocated
	for i := 0; int64(i) < leftover && i < len(parts); i++ {
		parts[i]++
	}
	return parts, nil
}
