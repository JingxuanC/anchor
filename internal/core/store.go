package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Quarantine struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type LoadResult struct {
	Events      []Event      `json:"-"`
	Quarantined []Quarantine `json:"quarantined"`
}

const CurrentEventLogVersion = 2

type Store struct {
	Dir             string
	EventLogVersion int
	mu              sync.Mutex
}

func Open(dir string) (*Store, error) {
	eventsDir := filepath.Join(dir, "events")
	if info, err := os.Lstat(eventsDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("events directory must not be a symlink")
	}
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		return nil, err
	}
	version := 0
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		var m struct {
			EventLogVersion int `json:"event_log_version"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("invalid workspace manifest: %w", err)
		}
		version = m.EventLogVersion
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if version < 0 || version > CurrentEventLogVersion {
		return nil, fmt.Errorf("unsupported workspace event_log_version %d (supported up to %d)", version, CurrentEventLogVersion)
	}
	return &Store{Dir: dir, EventLogVersion: version}, nil
}

func validateEventID(id string) error {
	if id == "" || id == "." || id == ".." || strings.HasPrefix(id, ".") || filepath.Base(id) != id {
		return fmt.Errorf("invalid event_id %q", id)
	}
	if strings.ContainsAny(id, "/\\:\x00") {
		return fmt.Errorf("invalid event_id %q", id)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid event_id %q", id)
		}
	}
	return nil
}

func (s *Store) eventsDir() string { return filepath.Join(s.Dir, "events") }

// LoadReport reads all valid events and quarantines individual corrupt files
// instead of failing the whole workspace. Valid events continue to replay.
func (s *Store) LoadReport() (LoadResult, error) {
	entries, err := os.ReadDir(s.eventsDir())
	if err != nil {
		return LoadResult{}, err
	}
	var out []Event
	var bad []Quarantine
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.eventsDir(), entry.Name()))
		if err != nil {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: err.Error()})
			continue
		}
		batch, reason := decodeBatchFile(b, s.EventLogVersion)
		if reason != "" {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: reason})
			continue
		}
		badReason := ""
		seenBatch := map[string]bool{}
		for _, e := range batch {
			h, err := e.Hash()
			if err != nil {
				badReason = "hash: " + err.Error()
				break
			}
			if e.EventHash == "" || e.EventHash != h {
				badReason = "hash mismatch"
				break
			}
			if seen[e.EventID] || seenBatch[e.EventID] {
				badReason = "duplicate event_id"
				break
			}
			seenBatch[e.EventID] = true
		}
		if badReason != "" {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: badReason})
			continue
		}
		for id := range seenBatch {
			seen[id] = true
		}
		out = append(out, batch...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EventID < out[j].EventID })
	return LoadResult{Events: out, Quarantined: bad}, nil
}

func decodeBatchFile(b []byte, version int) ([]Event, string) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return nil, "empty event file"
	}
	if trimmed[0] == '[' {
		return nil, fmt.Sprintf("legacy bare event array is not allowed (workspace event_log_version %d); use read-only event audit/migration dry-run", version)
	}
	if trimmed[0] != '{' {
		return nil, "parse: expected JSON object or array"
	}
	var probe struct {
		AnchorBatch int             `json:"anchor_batch"`
		BatchID     string          `json:"batch_id"`
		Events      json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, "parse: " + err.Error()
	}
	if probe.AnchorBatch == 0 && probe.BatchID == "" && len(probe.Events) == 0 {
		return nil, fmt.Sprintf("legacy single-event object is not allowed (workspace event_log_version %d); use read-only event audit/migration dry-run", version)
	}
	var env batchEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return nil, "batch parse: " + err.Error()
	}
	if env.AnchorBatch != 1 {
		return nil, "unsupported batch schema"
	}
	if !validBatchID(env.BatchID) {
		return nil, "invalid batch_id"
	}
	if env.EventCount <= 0 || env.EventCount != len(env.Events) {
		return nil, fmt.Sprintf("batch event_count mismatch: declared %d, actual %d", env.EventCount, len(env.Events))
	}
	h, err := batchHash(env.Events)
	if err != nil || env.EventsSHA256 != h {
		return nil, "batch manifest hash mismatch"
	}
	return env.Events, ""
}

func (s *Store) Load() ([]Event, error) {
	r, err := s.LoadReport()
	if err != nil {
		return nil, err
	}
	return r.Events, nil
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b))
}
