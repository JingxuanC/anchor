package names

import (
	"errors"
	"testing"
)

// Convention under test: trim, collapse internal whitespace runs to one space,
// preserve letter case.
func TestHiddenNormalizeSpaceCase(t *testing.T) {
	cases := map[string]string{
		"  Alice   Smith  ": "Alice Smith",
		"Bob":               "Bob",
		"a\t b\n c":         "a b c",
		"  Zoë  Lee ":       "Zoë Lee",
	}
	for in, want := range cases {
		got, err := NormalizeName(in)
		if err != nil {
			t.Fatalf("NormalizeName(%q) error: %v", in, err)
		}
		if got != want {
			t.Fatalf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, empty := range []string{"", "   ", "\t\n"} {
		if _, err := NormalizeName(empty); !errors.Is(err, ErrEmptyName) {
			t.Fatalf("NormalizeName(%q) = %v, want ErrEmptyName", empty, err)
		}
	}
}
