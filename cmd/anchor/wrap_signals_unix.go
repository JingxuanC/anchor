//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// wrapSignals lists the signals forwarded to the wrapped agent.
func wrapSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// exitCodeOf reports the child's exit status, using 128+signal when it was
// terminated by a forwarded signal.
func exitCodeOf(waitErr error, ps *os.ProcessState) int {
	if waitErr == nil {
		return 0
	}
	if ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	if ee, ok := waitErr.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}
