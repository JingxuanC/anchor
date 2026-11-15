package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirPlainLayoutIsTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	store, err := InitWorkspace(dir, "ws1", "dev1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := StateDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != store.Dir {
		t.Fatalf("StateDir(%q) = %q, want state root %q", dir, got, store.Dir)
	}
	if got != canonicalPath(t, dir) {
		t.Fatalf("StateDir(%q) = %q, want canonical %q", dir, got, canonicalPath(t, dir))
	}
}

func TestStateDirResolvesAttachedWorkspace(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("user content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stateDir, err := InitAttached(project, "ws1", "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(canonicalPath(t, project), ".anchor"); stateDir != want {
		t.Fatalf("state dir = %q, want %q", stateDir, want)
	}
	got, err := StateDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if got != stateDir {
		t.Fatalf("StateDir(project) = %q, want %q", got, stateDir)
	}
	// The state root itself stays addressable as a plain workspace directory.
	if again, err := StateDir(stateDir); err != nil || again != stateDir {
		t.Fatalf("StateDir(state) = %q, %v; want %q", again, err, stateDir)
	}
	// Opening through the project directory must reach the attached store.
	store, err := Open(project)
	if err != nil {
		t.Fatal(err)
	}
	if store.Dir != stateDir {
		t.Fatalf("Open(project).Dir = %q, want %q", store.Dir, stateDir)
	}
}

func TestInitAttachedLeavesProjectFilesUntouched(t *testing.T) {
	project := t.TempDir()
	readme := filepath.Join(project, "README.md")
	if err := os.WriteFile(readme, []byte("user content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, project)
	if _, _, err := InitAttached(project, "ws1", "dev1"); err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, project)
	for path, want := range before {
		if got, ok := after[path]; !ok || got != want {
			t.Fatalf("project file %q changed by attach: %q -> %q", path, want, got)
		}
	}
	b, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "user content\n" {
		t.Fatalf("README.md content changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(project, ".anchor", "manifest.json")); err != nil {
		t.Fatalf("attached state manifest missing: %v", err)
	}
}

func TestInitAttachedRefusesExistingState(t *testing.T) {
	project := t.TempDir()
	if _, _, err := InitAttached(project, "ws1", "dev1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitAttached(project, "ws2", "dev2"); err == nil {
		t.Fatal("second attach over existing state was accepted")
	} else if !strings.Contains(err.Error(), "already has Anchor state") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInitAttachedRequiresExistingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if _, _, err := InitAttached(missing, "ws1", "dev1"); err == nil {
		t.Fatal("attach to a missing project directory was accepted")
	}
}

func TestInitAttachedRejectsStateFileInPlaceOfDirectory(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".anchor"), []byte("not a dir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitAttached(project, "ws1", "dev1"); err == nil {
		t.Fatal("attach over a regular .anchor file was accepted")
	}
}

func TestStateDirRejectsHalfInitializedState(t *testing.T) {
	project := t.TempDir()
	state := filepath.Join(project, ".anchor")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "stray.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StateDir(project); err == nil {
		t.Fatal("half-initialized .anchor state was silently accepted")
	} else if !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStateDirIgnoresEmptyAnchorDirectory(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".anchor"), 0o700); err != nil {
		t.Fatal(err)
	}
	// An empty .anchor is not state: the directory falls back to the plain layout,
	// and attach is still allowed to initialize it.
	got, err := StateDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if got != canonicalPath(t, project) {
		t.Fatalf("StateDir = %q, want project dir %q", got, canonicalPath(t, project))
	}
	if _, _, err := InitAttached(project, "ws1", "dev1"); err != nil {
		t.Fatalf("attach into an empty .anchor failed: %v", err)
	}
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
