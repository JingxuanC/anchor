//go:build !anchor_test_hooks

package core

// Production builds contain no pause or fault-injection entry point.
func txnTestHookAfterFirstEvent(eventIndex, eventCount int) {}
