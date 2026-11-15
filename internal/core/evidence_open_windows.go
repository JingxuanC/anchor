//go:build windows

package core

import (
	"fmt"
	"os"
)

// Windows has no portable O_NOFOLLOW equivalent in this dependency-free core.
// Re-check symlink status immediately before opening; this is narrower than the
// Unix implementation and is not claimed as TOCTOU-proof.
func openEvidenceRead(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("evidence file must not be a symlink")
	}
	return os.Open(path)
}
