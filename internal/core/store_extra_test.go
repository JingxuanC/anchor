package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func manifestBytes(t *testing.T, version int) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		SchemaVersion   int    `json:"schema_version"`
		EventLogVersion int    `json:"event_log_version"`
		WorkspaceID     string `json:"workspace_id"`
		DeviceID        string `json:"device_id"`
	}{SchemaVersion: 1, EventLogVersion: version, WorkspaceID: "ws-test", DeviceID: "dev-test"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sealedEventFor builds a sealed, self-consistent ownership event for fixtures
// that are written straight to disk instead of going through Transact.
func sealedEventFor(t *testing.T, id, device string, seq uint64) Event {
	t.Helper()
	e := withID(rawOwn("n1", "l1", "agent", 1), id, device, seq)
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	return e
}

// writeBatchFile writes one manifested batch file exactly as the writer would.
func writeBatchFile(t *testing.T, eventsDir string, events []Event) {
	t.Helper()
	for i := range events {
		if events[i].EventHash == "" {
			if err := events[i].Seal(); err != nil {
				t.Fatal(err)
			}
		}
	}
	h, err := batchHash(events)
	if err != nil {
		t.Fatal(err)
	}
	env := batchEnvelope{
		AnchorBatch:  1,
		BatchID:      newID("batch"),
		EventCount:   len(events),
		EventsSHA256: h,
		Events:       events,
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(eventsDir, env.BatchID+".json")
	if err := os.WriteFile(name, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStateDirLayoutPrecedence(t *testing.T) {
	t.Run("plain empty directory", func(t *testing.T) {
		dir := t.TempDir()
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("StateDir = %q, want %q", got, canonicalPath(t, dir))
		}
	})
	t.Run("attached state with manifest", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, filepath.Join(dir, ".anchor", "manifest.json"), string(manifestBytes(t, 2)))
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(canonicalPath(t, dir), ".anchor"); got != want {
			t.Fatalf("StateDir = %q, want attached %q", got, want)
		}
	})
	t.Run("workspace root wins over nested anchor", func(t *testing.T) {
		dir := t.TempDir()
		initAt(t, dir)
		writeFileAt(t, filepath.Join(dir, ".anchor", "manifest.json"), string(manifestBytes(t, 2)))
		writeFileAt(t, filepath.Join(dir, ".anchor", "events", "nothing.json"), "{}")
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("StateDir = %q, want root workspace %q", got, canonicalPath(t, dir))
		}
	})
	t.Run("symlinked root manifest is not a workspace root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "manifest.json")
		writeFileAt(t, outside, string(manifestBytes(t, 2)))
		if err := os.Symlink(outside, filepath.Join(dir, "manifest.json")); err != nil {
			t.Fatal(err)
		}
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("symlinked manifest must not make %q a workspace root; got %q", dir, got)
		}
	})
	t.Run("unrelated manifest with events is not a workspace root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFileAt(t, filepath.Join(dir, "manifest.json"), `{"name":"webapp","version":1}`)
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("StateDir = %q, want plain %q", got, canonicalPath(t, dir))
		}
	})
	t.Run("corrupt manifest with events is not a workspace root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFileAt(t, filepath.Join(dir, "manifest.json"), "{not-json")
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("StateDir = %q, want plain %q", got, canonicalPath(t, dir))
		}
	})
	t.Run("oversized manifest with events is not a workspace root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFileAt(t, filepath.Join(dir, "manifest.json"), strings.Repeat(" ", 64<<10+1))
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("StateDir = %q, want plain %q", got, canonicalPath(t, dir))
		}
	})
	t.Run("events file is not a workspace root", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, filepath.Join(dir, "manifest.json"), string(manifestBytes(t, 2)))
		writeFileAt(t, filepath.Join(dir, "events"), "not a directory")
		got, err := StateDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalPath(t, dir) {
			t.Fatalf("StateDir = %q, want %q", got, canonicalPath(t, dir))
		}
	})
}

func TestStateDirRejectsInvalidLayouts(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		if _, err := StateDir(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("StateDir accepted a missing directory")
		}
	})
	t.Run("regular file path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		writeFileAt(t, path, "x")
		if _, err := StateDir(path); err == nil {
			t.Fatal("StateDir accepted a regular file")
		} else if !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "symlinked anchor directory",
			setup: func(t *testing.T, dir string) {
				target := filepath.Join(t.TempDir(), "state")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, ".anchor")); err != nil {
					t.Fatal(err)
				}
			},
			want: "is not a real directory",
		},
		{
			name: "anchor regular file",
			setup: func(t *testing.T, dir string) {
				writeFileAt(t, filepath.Join(dir, ".anchor"), "not a dir")
			},
			want: "is not a real directory",
		},
		{
			name: "symlinked attached manifest",
			setup: func(t *testing.T, dir string) {
				outside := filepath.Join(t.TempDir(), "manifest.json")
				writeFileAt(t, outside, string(manifestBytes(t, 2)))
				if err := os.MkdirAll(filepath.Join(dir, ".anchor"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, ".anchor", "manifest.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "must be a regular file, not a symlink",
		},
		{
			name: "attached manifest is a directory",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, ".anchor", "manifest.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "must be a regular file",
		},
		{
			name: "half-initialized attached state",
			setup: func(t *testing.T, dir string) {
				writeFileAt(t, filepath.Join(dir, ".anchor", "stray.json"), "{}")
			},
			want: "has no manifest.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			_, err := StateDir(dir)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestOpenErrorBranches(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "events is a regular file",
			setup: func(t *testing.T, dir string) {
				writeFileAt(t, filepath.Join(dir, "events"), "x")
			},
			want: "events directory must be a real directory",
		},
		{
			name: "events is a symlink",
			setup: func(t *testing.T, dir string) {
				target := filepath.Join(t.TempDir(), "events-target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "events")); err != nil {
					t.Fatal(err)
				}
				writeFileAt(t, filepath.Join(dir, "manifest.json"), string(manifestBytes(t, 2)))
			},
			want: "events directory must be a real directory",
		},
		{
			name: "missing events directory",
			setup: func(t *testing.T, dir string) {
				writeFileAt(t, filepath.Join(dir, "manifest.json"), string(manifestBytes(t, 2)))
			},
			want: "events directory is missing",
		},
		{
			name: "missing manifest",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "manifest.json is missing",
		},
		{
			name: "symlinked manifest",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "manifest.json")
				writeFileAt(t, outside, string(manifestBytes(t, 2)))
				if err := os.Symlink(outside, filepath.Join(dir, "manifest.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "manifest must be a regular file, not a symlink",
		},
		{
			name: "corrupt manifest",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFileAt(t, filepath.Join(dir, "manifest.json"), "{not-json")
			},
			want: "invalid workspace manifest",
		},
		{
			name: "future event log version",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFileAt(t, filepath.Join(dir, "manifest.json"), string(manifestBytes(t, CurrentEventLogVersion+1)))
			},
			want: "unsupported workspace event_log_version",
		},
		{
			name: "negative event log version",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFileAt(t, filepath.Join(dir, "manifest.json"), string(manifestBytes(t, -1)))
			},
			want: "unsupported workspace event_log_version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			if _, err := Open(dir); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	t.Run("path is a regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		writeFileAt(t, path, "x")
		if _, err := Open(path); err == nil {
			t.Fatal("Open accepted a regular file")
		}
	})
}

func TestOpenAcceptsSupportedEventLogVersions(t *testing.T) {
	for _, version := range []int{0, 1, CurrentEventLogVersion} {
		t.Run(versionName(version), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "events"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFileAt(t, filepath.Join(dir, "manifest.json"), string(manifestBytes(t, version)))
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if s.EventLogVersion != version {
				t.Fatalf("EventLogVersion = %d, want %d", s.EventLogVersion, version)
			}
		})
	}
}

func TestOpenResolvesAttachedLayout(t *testing.T) {
	project := t.TempDir()
	writeFileAt(t, filepath.Join(project, "README.md"), "user content\n")
	if _, _, err := InitAttached(project, "ws", "dev"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(project)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalPath(t, project), ".anchor")
	if s.Dir != want {
		t.Fatalf("Open(project).Dir = %q, want %q", s.Dir, want)
	}
}

func versionName(v int) string {
	return "version_" + string(rune('0'+v))
}

func TestInitWorkspaceRejectsResidualAnchorTraces(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string) error
	}{
		{"anchor directory", func(t *testing.T, dir string) error {
			return os.Mkdir(filepath.Join(dir, ".anchor"), 0o700)
		}},
		{"workspace lock", func(t *testing.T, dir string) error {
			return os.WriteFile(filepath.Join(dir, ".anchor.lock"), []byte("x"), 0o600)
		}},
		{"init lock", func(t *testing.T, dir string) error {
			return os.WriteFile(filepath.Join(dir, ".anchor.init.lock"), []byte("x"), 0o600)
		}},
		{"events directory", func(t *testing.T, dir string) error {
			return os.Mkdir(filepath.Join(dir, "events"), 0o700)
		}},
		{"manifest file", func(t *testing.T, dir string) error {
			return os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tc.setup(t, dir); err != nil {
				t.Fatal(err)
			}
			if _, err := InitWorkspace(dir, "ws", "dev"); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			} else if !strings.Contains(err.Error(), "existing Anchor data/traces") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	t.Run("regular file path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		writeFileAt(t, path, "x")
		if _, err := InitWorkspace(path, "ws", "dev"); err == nil {
			t.Fatal("init accepted a regular file")
		}
	})
	t.Run("creates nested missing path and cleans up lock", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "nested", "workspace")
		s, err := InitWorkspace(dir, "ws", "dev")
		if err != nil {
			t.Fatal(err)
		}
		if s.Dir != canonicalPath(t, dir) {
			t.Fatalf("store dir = %q, want %q", s.Dir, canonicalPath(t, dir))
		}
		if _, err := os.Stat(filepath.Join(dir, ".anchor.init.lock")); !os.IsNotExist(err) {
			t.Fatal("init lock was left behind")
		}
		manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			EventLogVersion int `json:"event_log_version"`
		}
		if err := json.Unmarshal(manifest, &m); err != nil {
			t.Fatal(err)
		}
		if m.EventLogVersion != CurrentEventLogVersion {
			t.Fatalf("manifest event_log_version = %d", m.EventLogVersion)
		}
	})
}

func TestInitAttachedRejectsSymlinkedStateDir(t *testing.T) {
	project := t.TempDir()
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(project, ".anchor")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitAttached(project, "ws", "dev"); err == nil {
		t.Fatal("attach accepted a symlinked state directory")
	} else if !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := InitAttached(filepath.Join(t.TempDir(), "absent"), "ws", "dev"); err == nil {
		t.Fatal("attach accepted a missing project")
	}
}

func TestEventBatchPaths(t *testing.T) {
	s := initTest(t)
	paths, err := s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("fresh workspace has batch files: %v", paths)
	}
	first := mustAppend(t, s, withID(rawOwn("n1", "l1", "a", 1), "batch-first", "d1", 1))
	second := mustAppend(t, s, withID(rawOwn("n2", "l2", "a", 1), "batch-second", "d1", 2))
	_ = first
	_ = second
	paths, err = s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 batch files, got %d: %v", len(paths), paths)
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("batch paths are not sorted: %v", paths)
	}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".json") {
			t.Fatalf("unexpected batch path %q", p)
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("batch path %q is not a regular file: %v", p, err)
		}
	}
	// Non-JSON files are ignored entirely.
	writeFileAt(t, filepath.Join(s.Dir, "events", "notes.txt"), "user notes")
	writeFileAt(t, filepath.Join(s.Dir, "events", "subdir", "nested.json"), "{}")
	paths, err = s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("non-json entries changed batch paths: %v", paths)
	}
	// A symlinked batch must be rejected, never followed.
	target := filepath.Join(t.TempDir(), "outside.json")
	writeFileAt(t, target, "{}")
	if err := os.Symlink(target, filepath.Join(s.Dir, "events", "zzz.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EventBatchPaths(); err == nil {
		t.Fatal("symlinked event batch was accepted")
	} else if !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	// A directory named *.json is also not a regular batch file.
	if err := os.Remove(filepath.Join(s.Dir, "events", "zzz.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Dir, "events", "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EventBatchPaths(); err == nil {
		t.Fatal("directory shaped like a batch was accepted")
	}
	// A store with no events directory surfaces the read error.
	if _, err := (&Store{Dir: t.TempDir()}).EventBatchPaths(); err == nil {
		t.Fatal("EventBatchPaths accepted a store without an events directory")
	}
}

func TestLoadReportQuarantinesDuplicateEventIDSeparately(t *testing.T) {
	s := initTest(t)
	dup := sealedEventFor(t, "dup-event", "d1", 1)
	writeBatchFile(t, filepath.Join(s.Dir, "events"), []Event{dup})
	writeBatchFile(t, filepath.Join(s.Dir, "events"), []Event{dup})
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 1 || len(r.Quarantined) != 1 {
		t.Fatalf("cross-file duplicate not quarantined: %+v", r)
	}
	if !strings.Contains(r.Quarantined[0].Reason, "duplicate event_id") {
		t.Fatalf("unexpected quarantine reason: %+v", r.Quarantined)
	}
	if r.Events[0].EventID != "dup-event" {
		t.Fatalf("first occurrence not preserved: %+v", r.Events)
	}
}

func TestLoadReportSkipsDirectoriesAndSortsEvents(t *testing.T) {
	s := initTest(t)
	writeBatchFile(t, filepath.Join(s.Dir, "events"), []Event{sealedEventFor(t, "zzz", "d1", 1)})
	writeBatchFile(t, filepath.Join(s.Dir, "events"), []Event{sealedEventFor(t, "aaa", "d1", 2)})
	writeFileAt(t, filepath.Join(s.Dir, "events", "ignore.txt"), "not an event")
	if err := os.Mkdir(filepath.Join(s.Dir, "events", "nested.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Quarantined) != 0 {
		t.Fatalf("unexpected quarantine: %+v", r.Quarantined)
	}
	if len(r.Events) != 2 || r.Events[0].EventID != "aaa" || r.Events[1].EventID != "zzz" {
		t.Fatalf("events not sorted by event_id: %+v", r.Events)
	}
	events, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventID != "aaa" {
		t.Fatalf("Load disagrees with LoadReport: %+v", events)
	}
	if _, err := (&Store{Dir: t.TempDir()}).LoadReport(); err == nil {
		t.Fatal("LoadReport accepted a store without an events directory")
	}
	if _, err := (&Store{Dir: t.TempDir()}).Load(); err == nil {
		t.Fatal("Load accepted a store without an events directory")
	}
}

func TestDecodeBatchFile(t *testing.T) {
	event := sealedEventFor(t, "evt-a", "d1", 1)
	second := sealedEventFor(t, "evt-b", "d1", 2)
	validEnv := func(events []Event) batchEnvelope {
		h, err := batchHash(events)
		if err != nil {
			t.Fatal(err)
		}
		return batchEnvelope{AnchorBatch: 1, BatchID: "batch_valid", EventCount: len(events), EventsSHA256: h, Events: events}
	}
	mustJSON := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	env := validEnv([]Event{event})
	tests := []struct {
		name    string
		data    []byte
		version int
		wantErr string
	}{
		{"empty bytes", nil, 2, "empty event file"},
		{"whitespace only", []byte(" \n\t "), 2, "empty event file"},
		{"legacy bare array", mustJSON([]Event{event}), 2, "legacy bare event array"},
		{"empty legacy bare array", []byte("[]"), 2, "legacy bare event array"},
		{"json string", []byte(`"hello"`), 2, "expected JSON object or array"},
		{"json number", []byte(`42`), 2, "expected JSON object or array"},
		{"malformed object", []byte(`{not-json`), 2, "parse:"},
		{"empty object is legacy single", []byte(`{}`), 2, "legacy single-event object"},
		{"single event object is legacy", mustJSON(event), 2, "legacy single-event object"},
		{"batch parse error", []byte(`{"anchor_batch":1,"batch_id":"batch_ok","events":"nope"}`), 2, "batch parse:"},
		{"invalid anchor_batch schema", mustJSON(func() batchEnvelope { e := env; e.AnchorBatch = 2; return e }()), 2, "unsupported batch schema"},
		{"invalid batch id", mustJSON(func() batchEnvelope { e := env; e.BatchID = "bad.id"; return e }()), 2, "invalid batch_id"},
		{"count mismatch", mustJSON(func() batchEnvelope { e := env; e.EventCount = 2; return e }()), 2, "event_count mismatch: declared 2, actual 1"},
		{"zero count", mustJSON(func() batchEnvelope { e := env; e.EventCount = 0; return e }()), 2, "event_count mismatch"},
		{"manifest hash mismatch", mustJSON(func() batchEnvelope {
			e := env
			e.EventsSHA256 = "sha256:" + strings.Repeat("0", 64)
			return e
		}()), 2, "batch manifest hash mismatch"},
		{"valid batch", mustJSON(env), 2, ""},
		{"valid multi-event batch", mustJSON(validEnv([]Event{event, second})), 2, ""},
		{"leading whitespace batch", append([]byte("\n  "), mustJSON(env)...), 2, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events, reason := decodeBatchFile(tc.data, tc.version)
			if tc.wantErr == "" {
				if reason != "" {
					t.Fatalf("valid batch rejected: %s", reason)
				}
				return
			}
			if reason == "" {
				t.Fatalf("invalid batch accepted with %d events", len(events))
			}
			if !strings.Contains(reason, tc.wantErr) {
				t.Fatalf("reason %q does not contain %q", reason, tc.wantErr)
			}
		})
	}
	t.Run("legacy message includes workspace version", func(t *testing.T) {
		_, reason := decodeBatchFile([]byte("[]"), 7)
		if !strings.Contains(reason, "event_log_version 7") {
			t.Fatalf("legacy reason %q does not include the workspace version", reason)
		}
	})
	t.Run("valid batch returns events", func(t *testing.T) {
		events, reason := decodeBatchFile(mustJSON(validEnv([]Event{event, second})), 2)
		if reason != "" {
			t.Fatal(reason)
		}
		if len(events) != 2 || events[0].EventID != "evt-a" || events[1].EventID != "evt-b" {
			t.Fatalf("decoded events = %+v", events)
		}
	})
}

func TestValidateEventID(t *testing.T) {
	valid := []string{"evt_1", "abc", "A-b_c.9", strings.Repeat("x", 200)}
	for _, id := range valid {
		if err := validateEventID(id); err != nil {
			t.Errorf("validateEventID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{
		"", ".", "..", ".hidden", "a/b", "a\\b", "a:b", "a\x00b", "a\nb", "a\x7fb",
	}
	for _, id := range invalid {
		if err := validateEventID(id); err == nil {
			t.Errorf("validateEventID(%q) = nil, want error", id)
		}
	}
}

func TestLoadReportQuarantinesUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of mode")
	}
	s := initTest(t)
	path := filepath.Join(s.Dir, "events", "locked.json")
	writeFileAt(t, path, string(mustMarshalJSON(t, []Event{sealedEventFor(t, "locked", "d1", 1)})))
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Quarantined) != 1 || r.Quarantined[0].File != "locked.json" {
		t.Fatalf("unreadable file not quarantined: %+v", r)
	}
	if r.Quarantined[0].Reason == "" {
		t.Fatal("quarantine reason is empty")
	}
}

func TestAuditEventFormatsReportsUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of mode")
	}
	s := initTest(t)
	path := filepath.Join(s.Dir, "events", "locked.json")
	writeFileAt(t, path, string(mustMarshalJSON(t, []Event{sealedEventFor(t, "locked-audit", "d1", 1)})))
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()
	audit, err := s.AuditEventFormats()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audit.InvalidFiles, []string{"locked.json"}) {
		t.Fatalf("unreadable file not classified invalid: %+v", audit)
	}
	if len(audit.Files) != 1 || audit.Files[0].Format != "invalid" || audit.Files[0].Reason == "" {
		t.Fatalf("unreadable file entry = %+v", audit.Files)
	}
}
