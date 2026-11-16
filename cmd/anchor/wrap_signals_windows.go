//go:build windows

package main

import (
	"os"
	"os/exec"
)

// wrapSignals lists the signals forwarded to the wrapped agent.
func wrapSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// exitCodeOf reports the child's exit status.
func exitCodeOf(waitErr error, ps *os.ProcessState) int {
	if waitErr == nil {
		return 0
	}
	if ee, ok := waitErr.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}
