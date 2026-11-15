package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const lockStaleAfter = 30 * time.Second

// acquireWorkspaceLock serializes the read-modify-write in Append across
// processes. O_CREATE|O_EXCL is atomic on local filesystems. A crashed process
// leaves a stale lock that is removed after lockStaleAfter; network filesystems
// with weak O_EXCL semantics are outside this guarantee.
func acquireWorkspaceLock(workspace string) (func(), error) {
	path := filepath.Join(workspace, ".anchor.lock")
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("workspace lock: %w", err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("workspace is locked by another process")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
