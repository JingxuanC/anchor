//go:build windows

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Windows fallback: exclusive-create lock without age-based stealing. A crashed
// process can leave a lock that requires manual removal; this is deliberately
// safer than stealing a live writer. A native LockFileEx implementation is a
// follow-up; it is not claimed as tested here.
func acquireWorkspaceLock(workspace string) (func(), error) {
	path := filepath.Join(workspace, ".anchor.lock")
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("workspace lock: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("workspace is locked by another process")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
