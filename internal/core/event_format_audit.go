package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type EventFormatFile struct {
	File   string `json:"file"`
	Format string `json:"format"`
	Events int    `json:"events"`
	Reason string `json:"reason,omitempty"`
}

type EventFormatAudit struct {
	ReadOnly            bool              `json:"read_only"`
	Writes              bool              `json:"writes"`
	MigrationDryRunOnly bool              `json:"migration_dry_run_only"`
	EventLogVersion     int               `json:"event_log_version"`
	Files               []EventFormatFile `json:"files"`
	LegacyFiles         []string          `json:"legacy_files,omitempty"`
	InvalidFiles        []string          `json:"invalid_files,omitempty"`
	Note                string            `json:"note"`
}

func classifyEventFile(b []byte) EventFormatFile {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return EventFormatFile{Format: "invalid", Reason: "empty file"}
	}
	if trimmed[0] == '[' {
		var events []Event
		if err := json.Unmarshal(trimmed, &events); err != nil {
			return EventFormatFile{Format: "invalid", Reason: "bare array parse: " + err.Error()}
		}
		return EventFormatFile{Format: "legacy_bare_array", Events: len(events)}
	}
	if trimmed[0] != '{' {
		return EventFormatFile{Format: "invalid", Reason: "expected JSON object or array"}
	}
	var probe struct {
		AnchorBatch int             `json:"anchor_batch"`
		BatchID     string          `json:"batch_id"`
		Events      json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return EventFormatFile{Format: "invalid", Reason: "parse: " + err.Error()}
	}
	if probe.AnchorBatch == 0 && probe.BatchID == "" && len(probe.Events) == 0 {
		var e Event
		if err := json.Unmarshal(trimmed, &e); err != nil {
			return EventFormatFile{Format: "invalid", Reason: "single object parse: " + err.Error()}
		}
		return EventFormatFile{Format: "legacy_single_object", Events: 1}
	}
	var env batchEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return EventFormatFile{Format: "invalid_manifest_batch", Reason: "batch parse: " + err.Error()}
	}
	if env.AnchorBatch != 1 {
		return EventFormatFile{Format: "invalid_manifest_batch", Events: len(env.Events), Reason: "unsupported batch schema"}
	}
	if !validBatchID(env.BatchID) {
		return EventFormatFile{Format: "invalid_manifest_batch", Events: len(env.Events), Reason: "invalid batch_id"}
	}
	if env.EventCount <= 0 || env.EventCount != len(env.Events) {
		return EventFormatFile{Format: "invalid_manifest_batch", Events: len(env.Events), Reason: fmt.Sprintf("batch event_count mismatch: declared %d, actual %d", env.EventCount, len(env.Events))}
	}
	h, err := batchHash(env.Events)
	if err != nil || env.EventsSHA256 != h {
		return EventFormatFile{Format: "invalid_manifest_batch", Events: len(env.Events), Reason: "batch manifest hash mismatch"}
	}
	return EventFormatFile{Format: "manifest_batch", Events: len(env.Events)}
}

// AuditEventFormats is read-only: it classifies files and never loads, rewrites,
// or migrates them. Legacy formats are diagnostics only.
func (s *Store) AuditEventFormats() (EventFormatAudit, error) {
	audit := EventFormatAudit{
		ReadOnly: true, Writes: false, MigrationDryRunOnly: true, EventLogVersion: s.EventLogVersion,
		Note: "read-only classification; no migration, rewrite, or event application is performed",
	}
	entries, err := os.ReadDir(s.eventsDir())
	if err != nil {
		return EventFormatAudit{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.eventsDir(), entry.Name()))
		if err != nil {
			audit.Files = append(audit.Files, EventFormatFile{File: entry.Name(), Format: "invalid", Reason: err.Error()})
			audit.InvalidFiles = append(audit.InvalidFiles, entry.Name())
			continue
		}
		f := classifyEventFile(b)
		f.File = entry.Name()
		audit.Files = append(audit.Files, f)
		if strings.HasPrefix(f.Format, "legacy_") {
			audit.LegacyFiles = append(audit.LegacyFiles, entry.Name())
		}
		if strings.HasPrefix(f.Format, "invalid") {
			audit.InvalidFiles = append(audit.InvalidFiles, entry.Name())
		}
	}
	sort.Slice(audit.Files, func(i, j int) bool { return audit.Files[i].File < audit.Files[j].File })
	sort.Strings(audit.LegacyFiles)
	sort.Strings(audit.InvalidFiles)
	return audit, nil
}
