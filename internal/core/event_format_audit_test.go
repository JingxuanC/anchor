package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestClassifyEventFile(t *testing.T) {
	eventA := sealedEventFor(t, "fmt-a", "d1", 1)
	eventB := sealedEventFor(t, "fmt-b", "d1", 2)
	envFor := func(events []Event) batchEnvelope {
		h, err := batchHash(events)
		if err != nil {
			t.Fatal(err)
		}
		return batchEnvelope{AnchorBatch: 1, BatchID: "batch_fmt", EventCount: len(events), EventsSHA256: h, Events: events}
	}
	mustJSON := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	env := envFor([]Event{eventA, eventB})
	tests := []struct {
		name       string
		data       []byte
		wantFormat string
		wantEvents int
		wantReason string
	}{
		{"empty", nil, "invalid", 0, "empty file"},
		{"whitespace", []byte(" \n"), "invalid", 0, "empty file"},
		{"bare array", mustJSON([]Event{eventA, eventB}), "legacy_bare_array", 2, ""},
		{"empty bare array", []byte("[]"), "legacy_bare_array", 0, ""},
		{"bare array parse error", []byte(`[{"event_id":`), "invalid", 0, "bare array parse:"},
		{"scalar json", []byte(`true`), "invalid", 0, "expected JSON object or array"},
		{"malformed object", []byte(`{not-json`), "invalid", 0, "parse:"},
		{"empty object", []byte(`{}`), "legacy_single_object", 1, ""},
		{"single event object", mustJSON(eventA), "legacy_single_object", 1, ""},
		{"single object parse error", []byte(`{"event_id":123}`), "invalid", 0, "single object parse:"},
		{"batch parse error", []byte(`{"anchor_batch":1,"batch_id":"batch_ok","events":"nope"}`), "invalid_manifest_batch", 0, "batch parse:"},
		{"valid manifest batch", mustJSON(env), "manifest_batch", 2, ""},
		{"unsupported batch schema", mustJSON(func() batchEnvelope {
			e := env
			e.AnchorBatch = 2
			return e
		}()), "invalid_manifest_batch", 2, "unsupported batch schema"},
		{"invalid batch id", mustJSON(func() batchEnvelope {
			e := env
			e.BatchID = "bad.id"
			return e
		}()), "invalid_manifest_batch", 2, "invalid batch_id"},
		{"count mismatch", mustJSON(func() batchEnvelope {
			e := env
			e.EventCount = 1
			return e
		}()), "invalid_manifest_batch", 2, "event_count mismatch: declared 1, actual 2"},
		{"hash mismatch", mustJSON(func() batchEnvelope {
			e := env
			e.EventsSHA256 = "sha256:" + strings.Repeat("f", 64)
			return e
		}()), "invalid_manifest_batch", 2, "batch manifest hash mismatch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyEventFile(tc.data)
			if got.Format != tc.wantFormat {
				t.Fatalf("format = %q, want %q", got.Format, tc.wantFormat)
			}
			if got.Events != tc.wantEvents {
				t.Fatalf("events = %d, want %d", got.Events, tc.wantEvents)
			}
			if tc.wantReason == "" {
				if got.Reason != "" {
					t.Fatalf("unexpected reason %q", got.Reason)
				}
				return
			}
			if !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("reason %q does not contain %q", got.Reason, tc.wantReason)
			}
			if got.File != "" {
				t.Fatalf("classifyEventFile set File = %q; that belongs to the audit", got.File)
			}
		})
	}
}

func TestAuditEventFormatsClassificationAndReadOnly(t *testing.T) {
	ws := t.TempDir()
	s := initAt(t, ws)
	eventsDir := filepath.Join(ws, "events")
	writeBatchFile(t, eventsDir, []Event{
		sealedEventFor(t, "audit-a", "d1", 1),
		sealedEventFor(t, "audit-b", "d1", 2),
	})
	if err := os.WriteFile(filepath.Join(eventsDir, "legacy-array.json"), mustMarshalJSON(t, []Event{sealedEventFor(t, "legacy-a", "d1", 1)}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventsDir, "legacy-single.json"), mustMarshalJSON(t, sealedEventFor(t, "legacy-s", "d1", 2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventsDir, "invalid.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventsDir, "empty.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventsDir, "notes.txt"), []byte("not an event"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(eventsDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, ws)
	audit, err := s.AuditEventFormats()
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, ws)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("AuditEventFormats mutated the workspace\nbefore=%v\nafter=%v", before, after)
	}
	if !audit.ReadOnly || audit.Writes || !audit.MigrationDryRunOnly {
		t.Fatalf("audit flags are wrong: %+v", audit)
	}
	if audit.EventLogVersion != CurrentEventLogVersion {
		t.Fatalf("event log version = %d, want %d", audit.EventLogVersion, CurrentEventLogVersion)
	}
	if audit.Note == "" {
		t.Fatal("audit note is empty")
	}
	if !sort.SliceIsSorted(audit.Files, func(i, j int) bool { return audit.Files[i].File < audit.Files[j].File }) {
		t.Fatalf("audit files are not sorted: %+v", audit.Files)
	}
	formats := map[string]string{
		"legacy-array.json":  "legacy_bare_array",
		"legacy-single.json": "legacy_single_object",
		"invalid.json":       "invalid",
		"empty.json":         "invalid",
	}
	for _, f := range audit.Files {
		if f.File == "notes.txt" || f.File == "subdir" {
			t.Fatalf("non-event entry %q was audited", f.File)
		}
		if want, ok := formats[f.File]; ok {
			if f.Format != want {
				t.Errorf("%s format = %q, want %q", f.File, f.Format, want)
			}
			continue
		}
		if f.Format != "manifest_batch" {
			t.Errorf("%s format = %q, want manifest_batch", f.File, f.Format)
		}
		if f.Events != 2 {
			t.Errorf("%s events = %d, want 2", f.File, f.Events)
		}
	}
	if len(audit.Files) != 5 {
		t.Fatalf("audited %d files, want 5: %+v", len(audit.Files), audit.Files)
	}
	wantLegacy := []string{"legacy-array.json", "legacy-single.json"}
	if !reflect.DeepEqual(audit.LegacyFiles, wantLegacy) {
		t.Fatalf("legacy files = %v, want %v", audit.LegacyFiles, wantLegacy)
	}
	wantInvalid := []string{"empty.json", "invalid.json"}
	if !reflect.DeepEqual(audit.InvalidFiles, wantInvalid) {
		t.Fatalf("invalid files = %v, want %v", audit.InvalidFiles, wantInvalid)
	}
	// The audit is idempotent and read-only on a second run.
	again, err := s.AuditEventFormats()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audit, again) {
		t.Fatalf("audit is not deterministic:\n%+v\n%+v", audit, again)
	}
}

func TestAuditEventFormatsMissingEventsDir(t *testing.T) {
	if _, err := (&Store{Dir: t.TempDir()}).AuditEventFormats(); err == nil {
		t.Fatal("audit accepted a store without an events directory")
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
