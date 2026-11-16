//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the agent in its own process group so a timeout
// can terminate the whole subtree (the agent CLI often spawns children).
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree kills the agent's process group, falling back to the direct
// process if the group is already gone.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
