package money

import "fmt"

// Allocate splits totalCents proportionally to weights.
// Convention: leftover cents go to the largest fractional remainders, ties to
// the lower index.
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
	rems := make([]int64, len(weights))
	var allocated int64
	for i, w := range weights {
		parts[i] = totalCents * w / sum
		rems[i] = totalCents * w % sum
		allocated += parts[i]
	}
	leftover := totalCents - allocated
	for n := int64(0); n < leftover; n++ {
		best := -1
		for i := range weights {
			if rems[i] < 0 {
				continue
			}
			if best == -1 || rems[i] > rems[best] {
				best = i
			}
		}
		if best < 0 {
			break
		}
		parts[best]++
		rems[best] = -1
	}
	return parts, nil
}
