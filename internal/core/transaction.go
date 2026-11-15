package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Tx is the only write path for workspace events. It sees one replay snapshot
// taken while the workspace lock is held; Add validates and stages events
// against that same snapshot. Nothing is written unless the callback returns
// nil and the final projection is complete.
type Tx struct {
	events    []Event
	staged    []Event
	result    ReplayResult
	nextSeq   map[string]uint64
	byID      map[string]Event
	idem      map[string]Event
	committed bool
}

func newTx(events []Event) *Tx {
	tx := &Tx{events: append([]Event(nil), events...), nextSeq: map[string]uint64{}, byID: map[string]Event{}, idem: map[string]Event{}}
	for _, e := range events {
		tx.byID[e.EventID] = e
		if e.DeviceSeq > tx.nextSeq[e.DeviceID] {
			tx.nextSeq[e.DeviceID] = e.DeviceSeq
		}
		if e.IdempotencyKey != "" {
			tx.idem[e.IdempotencyKey] = e
		}
	}
	tx.result = Replay(tx.events)
	return tx
}

func (tx *Tx) Tree() *Tree { return tx.result.Tree }

func (tx *Tx) FindIdempotent(key string) (Event, bool) {
	if key == "" {
		return Event{}, false
	}
	e, ok := tx.idem[key]
	return e, ok
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func rejectionReason(r ReplayResult, id string) string {
	for _, c := range r.Conflicts {
		if c.EventID == id {
			return c.Reason
		}
	}
	for _, p := range r.Pending {
		if p == id {
			return "event is pending on a missing causal parent"
		}
	}
	if !containsID(r.Applied, id) {
		return "event was not applied"
	}
	return ""
}

// Add stages e after validating it against the transaction snapshot and every
// previously staged event. It allocates a device sequence inside the locked
// transaction; a caller-supplied sequence must be exactly the next one.
func (tx *Tx) Add(e Event) (Event, error) {
	if tx.committed {
		return Event{}, fmt.Errorf("transaction is already committed")
	}
	if e.IdempotencyKey != "" {
		if _, exists := tx.idem[e.IdempotencyKey]; exists {
			return Event{}, fmt.Errorf("duplicate idempotency_key %q", e.IdempotencyKey)
		}
	}
	if e.EventID == "" {
		e.EventID = newID("evt")
	}
	if err := validateEventID(e.EventID); err != nil {
		return Event{}, err
	}
	if _, exists := tx.byID[e.EventID]; exists {
		return Event{}, fmt.Errorf("duplicate event_id %q", e.EventID)
	}
	if e.SchemaVersion == 0 {
		e.SchemaVersion = SchemaVersion
	}
	if e.RecordedAt.IsZero() {
		e.RecordedAt = time.Now().UTC()
	}
	next := tx.nextSeq[e.DeviceID] + 1
	if e.DeviceSeq == 0 {
		e.DeviceSeq = next
	}
	if e.DeviceSeq != next {
		return Event{}, fmt.Errorf("device_seq must be %d for device %q", next, e.DeviceID)
	}
	if e.EventHash != "" {
		h, err := e.Hash()
		if err != nil {
			return Event{}, err
		}
		if h != e.EventHash {
			return Event{}, fmt.Errorf("event_hash mismatch for %s", e.EventID)
		}
	}
	if err := e.Seal(); err != nil {
		return Event{}, err
	}
	r := Replay(append(append([]Event(nil), tx.events...), e))
	if reason := rejectionReason(r, e.EventID); reason != "" {
		return Event{}, fmt.Errorf("event %s rejected: %s", e.EventID, reason)
	}
	tx.events = append(tx.events, e)
	tx.staged = append(tx.staged, e)
	tx.byID[e.EventID] = e
	if e.IdempotencyKey != "" {
		tx.idem[e.IdempotencyKey] = e
	}
	tx.nextSeq[e.DeviceID] = e.DeviceSeq
	tx.result = r
	return e, nil
}

// Transact holds the process mutex and the cross-process workspace lock for the
// whole load -> replay -> validate -> allocate sequence -> append transaction.
func (s *Store) Transact(fn func(*Tx) error) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkspaceLock(s.Dir)
	if err != nil {
		return nil, err
	}
	defer release()
	report, err := s.LoadReport()
	if err != nil {
		return nil, err
	}
	if len(report.Quarantined) > 0 {
		return nil, fmt.Errorf("workspace has quarantined event files; refusing to write until recovery is complete")
	}
	tx := newTx(report.Events)
	if err := fn(tx); err != nil {
		return nil, err
	}
	if len(tx.staged) == 0 {
		return nil, nil
	}
	if tx.result.RecoveryStatus != "complete" || len(tx.result.Pending) > 0 || len(tx.result.Conflicts) > 0 {
		return nil, fmt.Errorf("transaction would leave an incomplete projection; refusing to write")
	}
	tx.committed = true
	if err := s.writeEvents(tx.staged); err != nil {
		return nil, err
	}
	return tx.staged, nil
}

type pendingWrite struct {
	tmp  string
	path string
}

func (s *Store) writeEvents(events []Event) error {
	var pending []pendingWrite
	cleanup := func() {
		for _, p := range pending {
			_ = os.Remove(p.tmp)
		}
	}
	for _, e := range events {
		b, err := json.MarshalIndent(e, "", "  ")
		if err != nil {
			cleanup()
			return err
		}
		f, err := os.CreateTemp(s.eventsDir(), ".event-*.tmp")
		if err != nil {
			cleanup()
			return err
		}
		tmp := f.Name()
		pending = append(pending, pendingWrite{tmp: tmp, path: filepath.Join(s.eventsDir(), e.EventID+".json")})
		if _, err := f.Write(b); err != nil {
			_ = f.Close()
			cleanup()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			cleanup()
			return err
		}
		if err := f.Close(); err != nil {
			cleanup()
			return err
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].path < pending[j].path })
	for i, p := range pending {
		if err := os.Rename(p.tmp, p.path); err != nil {
			for _, rest := range pending[i:] {
				_ = os.Remove(rest.tmp)
			}
			return err
		}
	}
	if d, err := os.Open(s.eventsDir()); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Append keeps the original one-event API but now routes it through the same
// locked, replay-validating transaction as every CLI mutation.
func (s *Store) Append(e Event) (Event, bool, error) {
	var out Event
	var created bool
	_, err := s.Transact(func(tx *Tx) error {
		if e.IdempotencyKey != "" {
			if existing, ok := tx.FindIdempotent(e.IdempotencyKey); ok {
				out = existing
				created = false
				return nil
			}
		}
		got, err := tx.Add(e)
		if err != nil {
			return err
		}
		out = got
		created = true
		return nil
	})
	return out, created, err
}
