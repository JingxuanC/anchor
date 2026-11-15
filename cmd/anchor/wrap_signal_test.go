//go:build !windows

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCLIWrapForwardsTerminationSignals proves the wrapped agent receives the
// signal (so it can flush its own state) and that wrap then exits promptly.
func TestCLIWrapForwardsTerminationSignals(t *testing.T) {
	_, ws, _ := wrapFixture(t)
	cmd := exec.Command(anchorBin, "wrap", "--dir", ws, "--node", "task1", "--json", "--",
		"sh", "-c", `trap 'echo got-term >&2; exit 0' TERM; while :; do sleep 0.1; done`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wrap exit after SIGTERM: %v (stderr=%s)", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("wrap did not exit after forwarding SIGTERM (stderr=%s)", stderr.String())
	}
	if !strings.Contains(stderr.String(), "got-term") {
		t.Fatalf("child did not receive the forwarded signal: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"exit_code": 0`) {
		t.Fatalf("wrap recorded the wrong exit code: %s", stderr.String())
	}
}
