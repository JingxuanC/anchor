package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	Dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	eventsDir := filepath.Join(dir, "events")
	if info, err := os.Lstat(eventsDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("events directory must not be a symlink")
	}
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
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

func (s *Store) Load() ([]Event, error) {
	entries, err := os.ReadDir(s.eventsDir())
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.eventsDir(), entry.Name()))
		if err != nil {
			return nil, err
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		h, err := e.Hash()
		if err != nil {
			return nil, err
		}
		if e.EventHash != "" && e.EventHash != h {
			return nil, fmt.Errorf("hash mismatch %s", e.EventID)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EventID < out[j].EventID })
	return out, nil
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b))
}

func (s *Store) Append(e Event) (Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.Load()
	if err != nil {
		return Event{}, false, err
	}
	if e.IdempotencyKey != "" {
		for _, existing := range events {
			if existing.IdempotencyKey == e.IdempotencyKey {
				return existing, false, nil
			}
		}
	}
	if e.EventID == "" {
		e.EventID = newID("evt")
	}
	if err := validateEventID(e.EventID); err != nil {
		return Event{}, false, err
	}
	if e.SchemaVersion == 0 {
		e.SchemaVersion = SchemaVersion
	}
	if e.RecordedAt.IsZero() {
		e.RecordedAt = time.Now().UTC()
	}
	var maxSeq uint64
	for _, existing := range events {
		if existing.DeviceID == e.DeviceID && existing.DeviceSeq > maxSeq {
			maxSeq = existing.DeviceSeq
		}
	}
	if e.DeviceSeq == 0 {
		e.DeviceSeq = maxSeq + 1
	}
	if e.DeviceSeq != maxSeq+1 {
		return Event{}, false, fmt.Errorf("device_seq must be %d for device %s", maxSeq+1, e.DeviceID)
	}
	for _, existing := range events {
		if existing.EventID == e.EventID {
			return Event{}, false, errors.New("duplicate event_id")
		}
	}
	if err := e.Seal(); err != nil {
		return Event{}, false, err
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return Event{}, false, err
	}
	path := filepath.Join(s.eventsDir(), e.EventID+".json")
	tmpFile, err := os.CreateTemp(s.eventsDir(), ".event-*.tmp")
	if err != nil {
		return Event{}, false, err
	}
	tmp := tmpFile.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := tmpFile.Write(b); err != nil {
		_ = tmpFile.Close()
		return Event{}, false, err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return Event{}, false, err
	}
	if err := tmpFile.Close(); err != nil {
		return Event{}, false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Event{}, false, err
	}
	return e, true, nil
}
