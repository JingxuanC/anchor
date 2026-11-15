package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceLockHelper(t *testing.T) {
	if os.Getenv("ANCHOR_LOCK_HELPER") != "1" {
		t.Skip("helper process only")
	}
	release, err := acquireWorkspaceLock(os.Getenv("ANCHOR_LOCK_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	release()
}

func TestWorkspaceLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestWorkspaceLockHelper")
	cmd.Env = append(os.Environ(), "ANCHOR_LOCK_HELPER=1", "ANCHOR_LOCK_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	lock := filepath.Join(dir, ".anchor.lock")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := rawOwn("n1", "l1", "a", 1)
	e.EventID = "locked"
	start := time.Now()
	if _, _, err := s.Append(e); err == nil {
		t.Fatal("append succeeded while another process held the lock")
	}
	if time.Since(start) < time.Second {
		t.Fatalf("lock wait was not cross-process: %v", time.Since(start))
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Append(e); err != nil {
		t.Fatalf("append failed after lock release: %v", err)
	}
}

func TestStaleWorkspaceLockRecovered(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, ".anchor.lock")
	if err := os.WriteFile(lock, []byte("dead"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	e := rawOwn("n1", "l1", "a", 1)
	e.EventID = "after-stale"
	if _, _, err := s.Append(e); err != nil {
		t.Fatalf("stale lock not recovered: %v", err)
	}
}

func TestAppendHelper(t *testing.T) {
	if os.Getenv("ANCHOR_APPEND_HELPER") != "1" {
		t.Skip("helper process only")
	}
	dir := os.Getenv("ANCHOR_APPEND_DIR")
	id := os.Getenv("ANCHOR_APPEND_ID")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := rawOwn("n1", "l1", "a", 1)
	e.EventID = id
	if _, _, err := s.Append(e); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAppenderAllocateUniqueSequence(t *testing.T) {
	dir := t.TempDir()
	var cmds []*exec.Cmd
	for i := 0; i < 4; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestAppendHelper")
		cmd.Env = append(os.Environ(), "ANCHOR_APPEND_HELPER=1", "ANCHOR_APPEND_DIR="+dir, "ANCHOR_APPEND_ID="+fmt.Sprintf("evt_%d", i))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("appender failed: %v", err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Quarantined) != 0 {
		t.Fatalf("quarantined events: %+v", r.Quarantined)
	}
	if len(r.Events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(r.Events))
	}
	seqs := map[uint64]bool{}
	for _, e := range r.Events {
		if seqs[e.DeviceSeq] {
			t.Fatalf("duplicate device_seq %d", e.DeviceSeq)
		}
		seqs[e.DeviceSeq] = true
	}
}
