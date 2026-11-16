//go:build windows

package main

import "os/exec"

// configureProcessGroup is a no-op on Windows; there is no POSIX process group.
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessTree kills the direct process.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
