//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// configureProcessGroup puts the child in a new process group on Windows.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killProcessTree terminates the child process on Windows.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
