package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// variantLeakedInRepo reports whether the phase-1 agent left the hidden
// convention recoverable from the repository (files or commit messages). That
// makes the phase-2 baseline able to pass without any durable session state, so
// leaked trials are reported separately rather than treated as continuity wins.
func variantLeakedInRepo(project string, variant *LoadedVariant) bool {
	needles := []string{strings.ToLower(variant.ID), strings.ToLower(variant.Phrase)}
	for _, n := range needles {
		if n == "" {
			continue
		}
		leaked := false
		_ = filepath.Walk(project, func(path string, info os.FileInfo, err error) error {
			if err != nil || leaked || info == nil {
				return nil
			}
			if info.IsDir() {
				switch info.Name() {
				case ".git", ".anchor":
					return filepath.SkipDir
				}
				return nil
			}
			if info.Size() > 2<<20 {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if strings.Contains(strings.ToLower(string(b)), n) {
				leaked = true
			}
			return nil
		})
		if leaked {
			return true
		}
		if out, err := exec.Command("git", "-C", project, "log", "--format=%B").Output(); err == nil {
			if strings.Contains(strings.ToLower(string(out)), n) {
				return true
			}
		}
	}
	return false
}
