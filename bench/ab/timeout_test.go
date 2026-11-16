//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExecAgentTimeoutKillsProcessGroup ensures a timed-out agent cannot leave
// grandchildren running (which used to keep the stdout pipe open and hang Wait).
func TestExecAgentTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "spawner.sh")
	body := "#!/bin/sh\nsleep 60 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Timeout: 300 * time.Millisecond}
	start := time.Now()
	res := execAgent(context.Background(), cfg, dir, []string{script}, nil, "", filepath.Join(dir, "agent.log"))
	elapsed := time.Since(start)
	if !res.TimedOut {
		t.Fatalf("expected a timeout, got %+v", res)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("timeout took %v; the process group was not killed", elapsed)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		// The child may not have started before the timeout; not a failure.
		t.Skipf("child pid not recorded: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// The grandchild must be gone (ESRCH), not merely orphaned.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived the timeout kill", pid)
}
