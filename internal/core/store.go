package core

import (
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

// LoadReport reads all valid events and quarantines individual corrupt files
// instead of failing the whole workspace. Valid events continue to replay.
func (s *Store) LoadReport() (LoadResult, error) {
	entries, err := os.ReadDir(s.eventsDir())
	if err != nil {
		return LoadResult{}, err
	}
	var out []Event
	var bad []Quarantine
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.eventsDir(), entry.Name()))
		if err != nil {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: err.Error()})
			continue
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: "parse: " + err.Error()})
			continue
		}
		h, err := e.Hash()
		if err != nil {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: "hash: " + err.Error()})
			continue
		}
		if e.EventHash != "" && e.EventHash != h {
			bad = append(bad, Quarantine{File: entry.Name(), Reason: "hash mismatch"})
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EventID < out[j].EventID })
	return LoadResult{Events: out, Quarantined: bad}, nil
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
