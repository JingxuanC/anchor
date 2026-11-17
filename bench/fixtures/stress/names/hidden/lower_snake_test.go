package names

import (
	"errors"
	"testing"
)

// Convention under test: trim, lowercase, replace internal whitespace runs with
// one underscore.
func TestHiddenNormalizeLowerSnake(t *testing.T) {
	cases := map[string]string{
		"  Alice   Smith  ": "alice_smith",
		"Bob":               "bob",
		"a\t b\n c":         "a_b_c",
		"  Zoë  Lee ":       "zoë_lee",
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
