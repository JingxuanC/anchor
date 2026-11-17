//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group so a timeout
// can kill the whole subtree (a surviving grandchild would keep pipes open).
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree kills the child's process group.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
