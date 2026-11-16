//go:build anchor_test_hooks

package core

import (
	"os"
	"time"
)

// This hook exists only in binaries built with -tags anchor_test_hooks. It is
// used by the real subprocess crash regression.
func txnTestHookAfterFirstEvent(eventIndex, eventCount int) {
	if eventIndex != 0 || eventCount < 2 || os.Getenv("ANCHOR_TEST_TXN_STOP_AFTER_EVENT") != "1" {
		return
	}
	if ready := os.Getenv("ANCHOR_TEST_TXN_READY"); ready != "" {
		_ = os.WriteFile(ready, []byte("paused"), 0o600)
	}
	time.Sleep(30 * time.Second)
}
