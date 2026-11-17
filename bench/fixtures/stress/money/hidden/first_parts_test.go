package money

import "testing"

// Convention under test: leftover cents go to the earliest parts in index
// order (part 0 first).
func TestHiddenAllocateFirstParts(t *testing.T) {
	cases := []struct {
		total   int64
		weights []int64
		want    []int64
	}{
		{10, []int64{1, 2, 4}, []int64{2, 3, 5}},
		{100, []int64{1, 1, 1}, []int64{34, 33, 33}},
		{7, []int64{2, 3, 5}, []int64{2, 2, 3}},
	}
	for _, c := range cases {
		got, err := Allocate(c.total, c.weights)
		if err != nil {
			t.Fatalf("Allocate(%d, %v) error: %v", c.total, c.weights, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("Allocate(%d, %v) = %v, want %v", c.total, c.weights, got, c.want)
		}
		var sum int64
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("Allocate(%d, %v) = %v, want %v", c.total, c.weights, got, c.want)
			}
			sum += got[i]
		}
		if sum != c.total {
			t.Fatalf("Allocate(%d, %v) sums to %d, want %d", c.total, c.weights, sum, c.total)
		}
	}
	for _, bad := range []struct {
		total   int64
		weights []int64
	}{{10, nil}, {10, []int64{}}, {10, []int64{1, 0}}, {10, []int64{1, -2}}, {-1, []int64{1}}} {
		if _, err := Allocate(bad.total, bad.weights); err == nil {
			t.Fatalf("Allocate(%d, %v) = nil error, want error", bad.total, bad.weights)
		}
	}
}
