package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// AdapterSessionID derives the stable Session Tree key from the adapter id and
// the already-hashed native session reference.
func AdapterSessionID(adapterID, sessionRef string) string {
	sum := sha256.Sum256([]byte("anchor-session|" + adapterID + "|" + sessionRef))
	return "sess_" + hex.EncodeToString(sum[:8])
}

func validHashRef(s string) bool {
	if !strings.HasPrefix(s, "sha256:") {
		return false
	}
	return validSHA256(strings.TrimPrefix(s, "sha256:"))
}

// Adapter session events are metadata only: they never acquire node ownership,
// never mutate node revision/status, and never mark a task done.
func validateAdapterSessionPayload(tree *Tree, e Event, p AdapterSessionPayload, ending bool) error {
	if p.SessionID == "" || p.SessionRef == "" || p.NodeID == "" || p.Adapter == "" || p.HookEvent == "" {
		return fmt.Errorf("adapter session requires session_id, session_ref, node_id, adapter and hook_event")
	}
	if !validHashRef(p.SessionRef) {
		return fmt.Errorf("adapter session_ref must be a sha256 hash")
	}
	if p.CwdRef != "" && !validHashRef(p.CwdRef) {
		return fmt.Errorf("adapter cwd_ref must be a sha256 hash")
	}
	if p.Source != "fixture" && p.Source != "hook_unverified" {
		return fmt.Errorf("adapter source must be fixture or hook_unverified")
	}
	if p.TrustLevel != "unverified_adapter" {
		return fmt.Errorf("adapter trust_level must be unverified_adapter")
	}
	for _, s := range []string{p.SessionID, p.SessionRef, p.NodeID, p.Adapter, p.HookEvent, p.Tool, p.CwdRef, p.Timestamp, p.Source, p.TrustLevel} {
		if err := ScanSecrets([]byte(s)); err != nil {
			return fmt.Errorf("adapter metadata rejected: %w", err)
		}
	}
	if p.SessionID != AdapterSessionID(p.Adapter, p.SessionRef) {
		return fmt.Errorf("adapter session_id does not match adapter/session_ref")
	}
	n, ok := tree.Nodes[p.NodeID]
	if !ok {
		return fmt.Errorf("adapter session node %s not found", p.NodeID)
	}
	if n.Deleted {
		return fmt.Errorf("adapter session node %s is tombstoned", p.NodeID)
	}
	if existing, ok := tree.Sessions[p.SessionID]; ok {
		if existing.Adapter != p.Adapter || existing.SessionRef != p.SessionRef || existing.NodeID != p.NodeID {
			return fmt.Errorf("adapter session mapping mismatch for %s", p.SessionID)
		}
		if ending && existing.Ended {
			return fmt.Errorf("adapter session %s is already ended", p.SessionID)
		}
	}
	if ending {
		if _, ok := tree.Sessions[p.SessionID]; !ok {
			return fmt.Errorf("adapter session %s has no observed event", p.SessionID)
		}
	}
	return nil
}
