// Package names validates and normalizes display names.
//
// This is the reference phase-1 implementation used by the offline mock agent.
// It is identical for every convention variant: the visible tests only exercise
// ValidateName, so they cannot pin the hidden normalization convention down.
package names

import (
	"errors"
	"strings"
)

// ErrEmptyName is returned when a name is empty after trimming whitespace.
var ErrEmptyName = errors.New("names: empty name")

// ValidateName reports whether s is an acceptable display name.
func ValidateName(s string) error {
	if strings.TrimSpace(s) == "" {
		return ErrEmptyName
	}
	return nil
}
