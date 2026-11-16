package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func eventFileHashes(t *testing.T, ws string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(ws, "events", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		out[filepath.Base(p)] = hex.EncodeToString(sum[:])
	}
	return out
}

func TestInitRefusesExistingAnchorDataWithoutTouchingLog(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	s, err := InitWorkspace(ws, "old-ws", "old-dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Append(Event{Type: EvOwnershipClaimed, CausalParents: []string{}, LeaseID: "l1", FencingToken: 1, Payload: json.RawMessage(`{"node_id":"n1","owner":"a","lease_id":"l1","fencing_token":1}`)}); err != nil {
		t.Fatal(err)
	}
	before := eventFileHashes(t, ws)
	manifestBefore, err := os.ReadFile(filepath.Join(ws, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(root, "manifest.saved.json")
	if err := os.Rename(filepath.Join(ws, "manifest.json"), saved); err != nil {
		t.Fatal(err)
	}
	if _, err := InitWorkspace(ws, "new-ws", "new-dev"); err == nil {
		t.Fatal("init rebound identity over existing event log")
	} else if !strings.Contains(err.Error(), "existing Anchor data") && !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("unexpected init error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("failed init repaired or recreated manifest.json")
	}
	after := eventFileHashes(t, ws)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("event log changed during rejected init\nbefore=%v\nafter=%v", before, after)
	}
	if _, err := os.Stat(filepath.Join(ws, ".anchor.init.lock")); !os.IsNotExist(err) {
		t.Fatal("init lock was left behind")
	}
	// Restoring the original manifest is the explicit recovery path; the original
	// identity and event log must then still open.
	if err := os.WriteFile(filepath.Join(ws, "manifest.json"), manifestBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ws); err != nil {
		t.Fatalf("original workspace did not reopen after manifest restore: %v", err)
	}
}

func TestInitRefusesResidualTracesAndOnlyInitializesEmptyDirs(t *testing.T) {
	cases := map[string]func(string) error{
		"events":   func(dir string) error { return os.Mkdir(filepath.Join(dir, "events"), 0o700) },
		"evidence": func(dir string) error { return os.Mkdir(filepath.Join(dir, "evidence"), 0o700) },
		"handoff":  func(dir string) error { return os.Mkdir(filepath.Join(dir, "handoff"), 0o700) },
		"manifest": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"schema_version":1}`), 0o600)
		},
		"lock": func(dir string) error { return os.WriteFile(filepath.Join(dir, ".anchor.lock"), []byte("x"), 0o600) },
		"init-lock": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, ".anchor.init.lock"), []byte("x"), 0o600)
		},
		"tmp":       func(dir string) error { return os.WriteFile(filepath.Join(dir, ".txn-stale.tmp"), []byte("x"), 0o600) },
		"unrelated": func(dir string) error { return os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := setup(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := InitWorkspace(dir, "ws", "dev"); err == nil {
				t.Fatalf("init accepted residual trace %s", name)
			}
			if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil && name != "manifest" {
				t.Fatal("failed init wrote manifest.json")
			}
		})
	}
	empty := t.TempDir()
	if _, err := InitWorkspace(empty, "ws", "dev"); err != nil {
		t.Fatalf("init on empty directory failed: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "new-workspace")
	if _, err := InitWorkspace(missing, "ws", "dev"); err != nil {
		t.Fatalf("init on missing path failed: %v", err)
	}
}
