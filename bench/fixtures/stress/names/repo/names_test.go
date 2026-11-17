package names

import (
	"errors"
	"testing"
)

func TestValidateName(t *testing.T) {
	if err := ValidateName("Alice"); err != nil {
		t.Errorf("ValidateName(%q) = %v, want nil", "Alice", err)
	}
	if err := ValidateName("Alice Smith"); err != nil {
		t.Errorf("ValidateName(%q) = %v, want nil", "Alice Smith", err)
	}
	for _, empty := range []string{"", "   ", "\t\n"} {
		if err := ValidateName(empty); !errors.Is(err, ErrEmptyName) {
			t.Errorf("ValidateName(%q) = %v, want ErrEmptyName", empty, err)
		}
	}
}
