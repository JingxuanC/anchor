package core

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			out[rel] = "symlink:" + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			out[rel] = "file:" + hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpenAndInitRejectSharedAgentPathsWithoutSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"user":"real-config"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configNested := filepath.Join(home, ".config", "claude", "nested")
	if err := os.MkdirAll(configNested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configNested, "settings.json"), []byte(`{"nested":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(home, ".codex")
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(claude, alias); err != nil {
		t.Fatal(err)
	}
	linkHome := filepath.Join(home, "link-home")
	if err := os.Symlink(home, linkHome); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, home)

	for _, path := range []string{claude, codex, alias, filepath.Join(claude, "nested"), filepath.Join(linkHome, ".claude"), configNested} {
		if _, err := Open(path); err == nil {
			t.Fatalf("Open accepted shared/potentially shared path %s", path)
		}
		if _, err := InitWorkspace(path, "ws", "dev"); err == nil {
			t.Fatalf("InitWorkspace accepted shared/potentially shared path %s", path)
		}
	}
	after := snapshotTree(t, home)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("shared path operations changed fake HOME\nbefore=%v\nafter=%v", before, after)
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatalf("nonexistent shared path was created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claude, "events")); !os.IsNotExist(err) {
		t.Fatal("events directory was created under ~/.claude")
	}
	if _, err := os.Stat(filepath.Join(claude, ".anchor.lock")); !os.IsNotExist(err) {
		t.Fatal("lock file was created under ~/.claude")
	}

	// Open never initializes an ordinary missing path, nor an existing empty dir.
	missing := filepath.Join(home, "missing-workspace")
	if _, err := Open(missing); err == nil {
		t.Fatal("Open initialized a missing workspace")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("failed Open created a workspace directory")
	}
	empty := t.TempDir()
	if _, err := Open(empty); err == nil {
		t.Fatal("Open accepted an uninitialized existing directory")
	}
	if _, err := os.Stat(filepath.Join(empty, "events")); !os.IsNotExist(err) {
		t.Fatal("failed Open created events/ in an empty directory")
	}

	// init may create a non-shared workspace, and only then does Open succeed.
	created := filepath.Join(home, "created-workspace")
	s, err := InitWorkspace(created, "ws1", "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if s.EventLogVersion != CurrentEventLogVersion {
		t.Fatalf("new workspace version=%d", s.EventLogVersion)
	}
	if _, err := Open(created); err != nil {
		t.Fatalf("Open failed on initialized workspace: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(created, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), `"event_log_version": 2`) {
		t.Fatalf("manifest missing version: %s %v", manifest, err)
	}
}
