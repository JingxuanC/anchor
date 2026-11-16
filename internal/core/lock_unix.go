//go:build unix

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// acquireWorkspaceLock uses an OS advisory flock. The lock is released
// automatically if the process crashes, so a live holder must never be stolen
// based on file age. The lock file itself may remain; flock owns the state.
func acquireWorkspaceLock(workspace string) (func(), error) {
	path := filepath.Join(workspace, ".anchor.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workspace lock: %w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = f.Close()
			return nil, fmt.Errorf("workspace lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("workspace is locked by another process")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
