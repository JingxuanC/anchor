// Package names validates and normalizes display names.
//
// It is deliberately small: the phase-1 tests below only exercise the phase-1
// validation API. How a later session must normalize names is decided in phase
// 1 and is not encoded anywhere in this repository.
package names

import "errors"

// ErrEmptyName is returned when a name is empty after trimming whitespace.
var ErrEmptyName = errors.New("names: empty name")

// ValidateName reports whether s is an acceptable display name.
// A name is acceptable when it contains at least one non-space character.
func ValidateName(s string) error {
	// TODO(phase 1): implement.
	return nil
}
