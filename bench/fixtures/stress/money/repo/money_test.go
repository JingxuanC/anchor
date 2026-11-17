package money

import "testing"

func TestFormatCents(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{105, "$1.05"},
		{250, "$2.50"},
		{-250, "-$2.50"},
		{123456, "$1234.56"},
	}
	for _, c := range cases {
		if got := FormatCents(c.cents); got != c.want {
			t.Errorf("FormatCents(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
}

func TestSumCents(t *testing.T) {
	if got := SumCents(); got != 0 {
		t.Errorf("SumCents() = %d, want 0", got)
	}
	if got := SumCents(5, -2, 100); got != 103 {
		t.Errorf("SumCents(5,-2,100) = %d, want 103", got)
	}
}
