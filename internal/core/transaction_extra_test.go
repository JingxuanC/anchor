package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTxTreeReflectsStagedSnapshotWithoutMutatingPriorSnapshots(t *testing.T) {
	s := initTest(t)
	seed(t, s)

	var before, afterClaim, afterCreate *Tree
	var created Event
	_, err := s.Transact(func(tx *Tx) error {
		before = tx.Tree()
		if before == nil {
			t.Fatal("Tx.Tree returned nil")
		}
		if _, ok := before.Nodes["g1"]; !ok {
			t.Fatalf("snapshot missing seeded goal: %+v", before.Nodes)
		}
		if _, ok := before.Nodes["n1"]; !ok {
			t.Fatalf("snapshot missing seeded task: %+v", before.Nodes)
		}
		claim, err := tx.Add(own("g2", "l2", "agent", 1))
		if err != nil {
			return err
		}
		afterClaim = tx.Tree()
		if afterClaim == before {
			t.Fatal("staging an event must publish a new projection pointer")
		}
		if _, ok := afterClaim.Nodes["g2"]; ok {
			t.Fatal("ownership of an unknown node must not create the node")
		}
		created, err = tx.Add(nodeCreate("g2", "goal", "goal two", "", "l2", 1, claim.EventID))
		if err != nil {
			return err
		}
		afterCreate = tx.Tree()
		if _, ok := afterCreate.Nodes["g2"]; !ok {
			t.Fatal("staged node_created not visible in tx.Tree")
		}
		if _, ok := before.Nodes["g2"]; ok {
			t.Fatal("earlier snapshot was mutated by a later Add")
		}
		if _, ok := afterClaim.Nodes["g2"]; ok {
			t.Fatal("earlier snapshot was mutated by node_created")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.EventID == "" {
		t.Fatal("Add returned an event without an id")
	}
	events, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("expected 6 committed events, got %d", len(events))
	}
}

func TestTxFindIdempotent(t *testing.T) {
	s := initTest(t)
	e := withID(rawOwn("n1", "l1", "a", 1), "idem-event", "d1", 1)
	e.IdempotencyKey = "idem-key-1"
	first, created, err := s.Append(e)
	if err != nil || !created {
		t.Fatalf("setup append: created=%v err=%v", created, err)
	}
	_, err = s.Transact(func(tx *Tx) error {
		got, ok := tx.FindIdempotent("idem-key-1")
		if !ok {
			t.Fatal("committed idempotency key not found")
		}
		if got.EventID != first.EventID {
			t.Fatalf("FindIdempotent returned %q, want %q", got.EventID, first.EventID)
		}
		if _, ok := tx.FindIdempotent("unknown-key"); ok {
			t.Fatal("unknown idempotency key found")
		}
		if _, ok := tx.FindIdempotent(""); ok {
			t.Fatal("empty idempotency key must never match")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTxAddRejections(t *testing.T) {
	s := initTest(t)
	claim, created := seed(t, s)
	preKey := withID(rawOwn("pre-key", "l", "a", 1), "pre-key-event", "d1", 1)
	preKey.IdempotencyKey = "taken-key"
	if _, created, err := s.Append(preKey); err != nil || !created {
		t.Fatalf("setup idempotency event: created=%v err=%v", created, err)
	}
	preID := withID(rawOwn("pre-id", "l", "a", 1), "taken-id", "d1", 2)
	if _, _, err := s.Append(preID); err != nil {
		t.Fatal(err)
	}
	_ = claim

	tests := []struct {
		name string
		add  func(tx *Tx) error
		want string
	}{
		{
			name: "duplicate idempotency key",
			add: func(tx *Tx) error {
				e := own("n9", "l9", "a", 1)
				e.IdempotencyKey = "taken-key"
				_, err := tx.Add(e)
				return err
			},
			want: "duplicate idempotency_key",
		},
		{
			name: "duplicate event id",
			add: func(tx *Tx) error {
				e := withID(own("n9", "l9", "a", 1), "taken-id", "d1", 0)
				_, err := tx.Add(e)
				return err
			},
			want: "duplicate event_id",
		},
		{
			name: "invalid event id",
			add: func(tx *Tx) error {
				e := withID(own("n9", "l9", "a", 1), "../escape", "d1", 0)
				_, err := tx.Add(e)
				return err
			},
			want: "invalid event_id",
		},
		{
			name: "device sequence gap",
			add: func(tx *Tx) error {
				e := withID(own("n9", "l9", "a", 1), "", "d1", 99)
				_, err := tx.Add(e)
				return err
			},
			want: "device_seq must be",
		},
		{
			name: "event hash mismatch",
			add: func(tx *Tx) error {
				e := own("n9", "l9", "a", 1)
				e.EventHash = "sha256:" + strings.Repeat("0", 64)
				_, err := tx.Add(e)
				return err
			},
			want: "event_hash mismatch",
		},
		{
			name: "missing causal parent is pending",
			add: func(tx *Tx) error {
				e := own("n9", "l9", "a", 1, "never-written")
				_, err := tx.Add(e)
				return err
			},
			want: "pending on a missing causal parent",
		},
		{
			name: "base revision conflict",
			add: func(tx *Tx) error {
				e := status("n1", StatusInProgress, 99, "l1", 1, created.EventID)
				_, err := tx.Add(e)
				return err
			},
			want: "revision conflict",
		},
		{
			name: "expected revision conflict",
			add: func(tx *Tx) error {
				e := status("n1", StatusInProgress, 1, "l1", 1, created.EventID)
				e.ExpectedRevision = 99
				_, err := tx.Add(e)
				return err
			},
			want: "expected_revision conflict",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before, err := s.LoadReport()
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Transact(func(tx *Tx) error { return tc.add(tx) })
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			after, err := s.LoadReport()
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Events) != len(before.Events) {
				t.Fatalf("rejected add wrote events: before=%d after=%d", len(before.Events), len(after.Events))
			}
			if len(after.Quarantined) != 0 {
				t.Fatalf("rejected add left quarantine: %+v", after.Quarantined)
			}
		})
	}
}

func TestTxAddAfterCommitRejected(t *testing.T) {
	s := initTest(t)
	var captured *Tx
	_, err := s.Transact(func(tx *Tx) error {
		captured = tx
		_, err := tx.Add(own("n1", "l1", "a", 1))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captured.Add(own("n2", "l2", "a", 1)); err == nil {
		t.Fatal("Add accepted an event after commit")
	} else if !strings.Contains(err.Error(), "already committed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTxAddAllocatesIdentityAndSequence(t *testing.T) {
	s := initTest(t)
	var got []Event
	_, err := s.Transact(func(tx *Tx) error {
		for i := 0; i < 2; i++ {
			e := own("n1", "l1", "a", 1)
			e.DeviceID = "device-x"
			if i > 0 {
				e.CausalParents = []string{got[i-1].EventID}
			}
			added, err := tx.Add(e)
			if err != nil {
				return err
			}
			got = append(got, added)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	for i, e := range got {
		if !strings.HasPrefix(e.EventID, "evt_") {
			t.Fatalf("event id %q was not allocated", e.EventID)
		}
		if e.SchemaVersion != SchemaVersion {
			t.Fatalf("schema version = %d, want %d", e.SchemaVersion, SchemaVersion)
		}
		if e.RecordedAt.IsZero() {
			t.Fatal("recorded_at was not allocated")
		}
		if e.DeviceSeq != uint64(i+1) {
			t.Fatalf("device_seq = %d, want %d", e.DeviceSeq, i+1)
		}
	}
}

func TestTransactAllOrNothing(t *testing.T) {
	s := initTest(t)
	boom := errors.New("callback boom")
	if _, err := s.Transact(func(tx *Tx) error {
		if _, err := tx.Add(own("n1", "l1", "a", 1)); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("callback error not propagated: %v", err)
	}
	paths, err := s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("failed transaction wrote batch files: %v", paths)
	}
	report, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Events) != 0 {
		t.Fatalf("failed transaction wrote events: %+v", report.Events)
	}

	// A staged event followed by a rejected one must not be committed either.
	if _, err := s.Transact(func(tx *Tx) error {
		if _, err := tx.Add(own("n1", "l1", "a", 1)); err != nil {
			return err
		}
		bad := withID(own("n2", "l2", "a", 1), "", "d1", 42)
		_, err := tx.Add(bad)
		return err
	}); err == nil {
		t.Fatal("invalid staged event was accepted")
	}
	report, err = s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Events) != 0 {
		t.Fatalf("partial transaction was written: %+v", report.Events)
	}
	paths, err = s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("partial transaction left batch files: %v", paths)
	}

	// No staged events is a no-op, not an error.
	events, err := s.Transact(func(tx *Tx) error { return nil })
	if err != nil {
		t.Fatalf("empty transaction failed: %v", err)
	}
	if events != nil {
		t.Fatalf("empty transaction returned events: %+v", events)
	}
}

func TestTransactRefusesQuarantinedWorkspace(t *testing.T) {
	s := initTest(t)
	bad := filepath.Join(s.Dir, "events", "corrupt.json")
	writeFileAt(t, bad, "{not-json")
	if _, err := s.Transact(func(tx *Tx) error {
		_, err := tx.Add(own("n1", "l1", "a", 1))
		return err
	}); err == nil {
		t.Fatal("transact accepted a quarantined workspace")
	} else if !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Fatal("refused transaction removed the quarantined file")
	}
	paths, err := s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "corrupt.json" {
		t.Fatalf("refused transaction wrote a batch: %v", paths)
	}
}

func TestCleanupStaleTemps(t *testing.T) {
	s := initTest(t)
	eventsDir := filepath.Join(s.Dir, "events")
	stale := []string{".txn-abc.tmp", ".event-xyz.tmp"}
	kept := []string{"keep.tmp", "keep.txt", ".event-keep.notatmp"}
	for _, name := range append(append([]string{}, stale...), kept...) {
		writeFileAt(t, filepath.Join(eventsDir, name), "x")
	}
	staleDir := filepath.Join(eventsDir, ".txn-dir.tmp")
	if err := os.Mkdir(staleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transact(func(tx *Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(eventsDir, name)); !os.IsNotExist(err) {
			t.Fatalf("stale temp %s was not removed", name)
		}
	}
	for _, name := range kept {
		if _, err := os.Stat(filepath.Join(eventsDir, name)); err != nil {
			t.Fatalf("non-stale file %s was removed: %v", name, err)
		}
	}
	if info, err := os.Stat(staleDir); err != nil || !info.IsDir() {
		t.Fatalf("stale temp directory was removed: %v", err)
	}
	if err := (&Store{Dir: t.TempDir()}).cleanupStaleTemps(); err == nil {
		t.Fatal("cleanupStaleTemps accepted a store without an events directory")
	}
	if err := s.writeEvents(nil); err != nil {
		t.Fatalf("writeEvents(nil) = %v, want nil", err)
	}
}

func TestWriteEventsFailureLeavesNoBatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	s := initTest(t)
	eventsDir := filepath.Join(s.Dir, "events")
	if err := os.Chmod(eventsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(eventsDir, 0o700); err != nil {
			t.Fatalf("restore events permissions: %v", err)
		}
	}()
	_, err := s.Transact(func(tx *Tx) error {
		_, err := tx.Add(own("n1", "l1", "a", 1))
		return err
	})
	if err == nil {
		t.Fatal("transaction succeeded with a read-only events directory")
	}
	if err := os.Chmod(eventsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := s.EventBatchPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("failed write left a batch file: %v", paths)
	}
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("failed write left a temp file: %q", entry.Name())
		}
	}
}

func TestAppendIdempotency(t *testing.T) {
	s := initTest(t)
	e := withID(rawOwn("n1", "l1", "a", 1), "idem-append", "d1", 1)
	e.IdempotencyKey = "append-key"
	first, created, err := s.Append(e)
	if err != nil || !created {
		t.Fatalf("first append: created=%v err=%v", created, err)
	}
	again, created, err := s.Append(e)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("replayed idempotency key reported created=true")
	}
	if again.EventID != first.EventID {
		t.Fatalf("replay returned %q, want %q", again.EventID, first.EventID)
	}
	report, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Events) != 1 {
		t.Fatalf("idempotent append wrote %d events, want 1", len(report.Events))
	}
	invalid := withID(own("n2", "l2", "a", 1), "../escape", "d1", 2)
	if _, _, err := s.Append(invalid); err == nil {
		t.Fatal("Append accepted an invalid event id")
	}
}

func TestBatchHashAndValidBatchID(t *testing.T) {
	e1 := withID(rawOwn("n1", "l1", "a", 1), "h1", "d1", 1)
	e2 := withID(rawOwn("n2", "l2", "a", 1), "h2", "d1", 2)
	h1, err := batchHash([]Event{e1})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := batchHash([]Event{e1})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("batch hash is not deterministic")
	}
	if !strings.HasPrefix(h1, "sha256:") || len(h1) != len("sha256:")+64 {
		t.Fatalf("unexpected batch hash shape %q", h1)
	}
	h3, err := batchHash([]Event{e1, e2})
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatal("batch hash ignores event content")
	}
	if _, err := batchHash([]Event{{Payload: json.RawMessage("{not-json")}}); err == nil {
		t.Fatal("batchHash accepted an unencodable event")
	}

	valid := map[string]bool{
		"a":                             true,
		"batch_123-ABC":                 true,
		strings.Repeat("a", 128):        true,
		"":                              false,
		"bad id":                        false,
		"bad.id":                        false,
		"slash/":                        false,
		"üñî":                           false,
		strings.Repeat("a", 129):        false,
		"batch:with_colon":              false,
		"tab\tchar":                     false,
		"trailing\n":                    false,
		"BATCH-ok_9":                    true,
		strings.Repeat("9", 128):        true,
		"dash-and_underscore-only_ok":   true,
		"mixedCase1234567890abcdefghij": true,
	}
	for id, want := range valid {
		if got := validBatchID(id); got != want {
			t.Errorf("validBatchID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestRejectionReason(t *testing.T) {
	r := ReplayResult{
		Conflicts: []Conflict{{EventID: "c1", Reason: "conflict reason"}},
		Pending:   []string{"p1"},
		Applied:   []string{"a1"},
	}
	if got := rejectionReason(r, "c1"); got != "conflict reason" {
		t.Fatalf("conflict reason = %q", got)
	}
	if got := rejectionReason(r, "p1"); !strings.Contains(got, "pending") {
		t.Fatalf("pending reason = %q", got)
	}
	if got := rejectionReason(r, "unknown"); got != "event was not applied" {
		t.Fatalf("not-applied reason = %q", got)
	}
	if got := rejectionReason(r, "a1"); got != "" {
		t.Fatalf("applied event reason = %q, want empty", got)
	}
}

func TestTransactFailsWhenLockCannotBeCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := initTest(t)
	if err := os.Chmod(s.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(s.Dir, 0o700) }()
	if _, err := s.Transact(func(tx *Tx) error { return nil }); err == nil {
		t.Fatal("transaction succeeded without a workspace lock")
	}
	// Restore before the test framework removes the directory.
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
}
