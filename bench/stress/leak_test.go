package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVariantLeakDetection(t *testing.T) {
	dir := t.TempDir()
	v := LoadedVariant{ID: "largest_remainder", Phrase: "largest fractional remainder"}
	if variantLeakedInRepo(dir, &v) {
		t.Fatal("empty repo reported a leak")
	}
	if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("use the largest_remainder convention"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !variantLeakedInRepo(dir, &v) {
		t.Fatal("leak in a file was not detected")
	}
	// .anchor state is durable session state, not a repo leak.
	clean := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clean, ".anchor", "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, ".anchor", "events", "x.json"), []byte("largest_remainder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if variantLeakedInRepo(clean, &v) {
		t.Fatal(".anchor state must not count as a repo leak")
	}
}
