package core

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// hashRefFor derives a stable sha256: hash reference for arbitrary fixture text.
func hashRefFor(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestAdapterSessionIDDerivation(t *testing.T) {
	refA := "sha256:" + strings.Repeat("a", 64)
	refB := "sha256:" + strings.Repeat("b", 64)
	// Fixed vectors pin the derivation: session state must be derivable from the
	// adapter id and the already-hashed native session reference alone.
	cases := []struct {
		adapter string
		ref     string
		want    string
	}{
		{"claude-code", refA, "sess_fed1582f1dd29548"},
		{"codex", refA, "sess_ca5d9660b2c0a88f"},
	}
	for _, tc := range cases {
		if got := AdapterSessionID(tc.adapter, tc.ref); got != tc.want {
			t.Errorf("AdapterSessionID(%q, %q) = %q, want %q", tc.adapter, tc.ref, got, tc.want)
		}
	}
	got := AdapterSessionID("claude-code", refA)
	if len(got) != len("sess_")+16 || !strings.HasPrefix(got, "sess_") {
		t.Fatalf("session id shape %q", got)
	}
	if AdapterSessionID("claude-code", refA) != got {
		t.Fatal("session id derivation is not deterministic")
	}
	if AdapterSessionID("codex", refA) == got {
		t.Fatal("adapter id must participate in the session id")
	}
	if AdapterSessionID("claude-code", refB) == got {
		t.Fatal("session reference must participate in the session id")
	}
	// Any adapter string is derived deterministically; adapters are open-ended.
	if AdapterSessionID("future-adapter", refA) != AdapterSessionID("future-adapter", refA) {
		t.Fatal("unknown adapter derivation is not deterministic")
	}
}

func TestValidHashRef(t *testing.T) {
	valid := []string{
		"sha256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("0", 64),
	}
	for _, s := range valid {
		if !validHashRef(s) {
			t.Errorf("validHashRef(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",
		"sha256",
		"sha256:",
		"SHA256:" + strings.Repeat("a", 64),
		" sha256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		"sha256:" + strings.Repeat("a", 63) + "g",
		"sha256:sha256:" + strings.Repeat("a", 56),
	}
	for _, s := range invalid {
		if validHashRef(s) {
			t.Errorf("validHashRef(%q) = true, want false", s)
		}
	}
}

func newSessionTree() *Tree {
	tree := NewTree()
	tree.Nodes["n1"] = &Node{ID: "n1", Type: NodeTask, Title: "task", Revision: 1}
	return tree
}

func baseAdapterPayload() AdapterSessionPayload {
	ref := hashRefFor("session-1")
	return AdapterSessionPayload{
		SessionID:      AdapterSessionID("claude-code", ref),
		SessionRef:     ref,
		NodeID:         "n1",
		Adapter:        "claude-code",
		HookEvent:      "SessionStart",
		Tool:           "Bash",
		CwdRef:         hashRefFor("cwd"),
		Timestamp:      "2026-01-01T00:00:00Z",
		Source:         "fixture",
		TrustLevel:     "unverified_adapter",
		SourceEventRef: hashRefFor("source-event-1"),
	}
}

func TestValidateAdapterSessionPayloadAcceptsValidObservations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *AdapterSessionPayload)
	}{
		{"full fixture payload", func(p *AdapterSessionPayload) {}},
		{"hook_unverified source", func(p *AdapterSessionPayload) { p.Source = "hook_unverified" }},
		{"no optional cwd ref", func(p *AdapterSessionPayload) { p.CwdRef = "" }},
		{"unknown hook event", func(p *AdapterSessionPayload) { p.HookEvent = "FutureHookEvent" }},
		{"unknown adapter", func(p *AdapterSessionPayload) {
			p.Adapter = "future-adapter"
			p.SessionID = AdapterSessionID(p.Adapter, p.SessionRef)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := newSessionTree()
			p := baseAdapterPayload()
			tc.mutate(&p)
			if err := validateAdapterSessionPayload(tree, Event{EventID: "e1"}, p, false); err != nil {
				t.Fatalf("valid payload rejected: %v", err)
			}
		})
	}
}

func TestValidateAdapterSessionPayloadEndingRequiresObservedSession(t *testing.T) {
	tree := newSessionTree()
	p := baseAdapterPayload()
	if err := validateAdapterSessionPayload(tree, Event{EventID: "end"}, p, true); err == nil {
		t.Fatal("ending an unobserved session was accepted")
	} else if !strings.Contains(err.Error(), "has no observed event") {
		t.Fatalf("unexpected error: %v", err)
	}
	tree.Sessions[p.SessionID] = Session{
		ID: p.SessionID, SessionRef: p.SessionRef, Adapter: p.Adapter, NodeID: p.NodeID,
	}
	if err := validateAdapterSessionPayload(tree, Event{EventID: "end"}, p, true); err != nil {
		t.Fatalf("ending an observed session was rejected: %v", err)
	}
}

func TestValidateAdapterSessionPayloadRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(tree *Tree, p *AdapterSessionPayload)
		ending bool
		want   string
	}{
		{
			name:   "missing session id",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.SessionID = "" },
			want:   "session_id, session_ref, node_id, adapter and hook_event",
		},
		{
			name:   "missing session ref",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.SessionRef = "" },
			want:   "session_id, session_ref, node_id, adapter and hook_event",
		},
		{
			name:   "missing node id",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.NodeID = "" },
			want:   "session_id, session_ref, node_id, adapter and hook_event",
		},
		{
			name:   "missing adapter",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.Adapter = "" },
			want:   "session_id, session_ref, node_id, adapter and hook_event",
		},
		{
			name:   "missing hook event",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.HookEvent = "" },
			want:   "session_id, session_ref, node_id, adapter and hook_event",
		},
		{
			name:   "missing source event ref",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.SourceEventRef = "" },
			want:   "source_event_ref must be a sha256 hash",
		},
		{
			name:   "malformed source event ref",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.SourceEventRef = "sha256:short" },
			want:   "source_event_ref must be a sha256 hash",
		},
		{
			name:   "session ref without hash prefix",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.SessionRef = strings.Repeat("a", 64) },
			want:   "session_ref must be a sha256 hash",
		},
		{
			name:   "session ref wrong length",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.SessionRef = "sha256:" + strings.Repeat("a", 62) },
			want:   "session_ref must be a sha256 hash",
		},
		{
			name:   "malformed cwd ref",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.CwdRef = "not-a-hash" },
			want:   "cwd_ref must be a sha256 hash",
		},
		{
			name:   "unknown source",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.Source = "hook_verified" },
			want:   "adapter source must be fixture or hook_unverified",
		},
		{
			name:   "unknown trust level",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.TrustLevel = "verified" },
			want:   "adapter trust_level must be unverified_adapter",
		},
		{
			name:   "secret in tool metadata",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.Tool = "ghp_1234567890abcdefghijklmnopqrstuvwx" },
			want:   "adapter metadata rejected",
		},
		{
			name: "secret in timestamp metadata",
			mutate: func(_ *Tree, p *AdapterSessionPayload) {
				p.Timestamp = "token=ghp_1234567890abcdefghijklmnopqrstuvwx"
			},
			want: "adapter metadata rejected",
		},
		{
			name: "session id does not match adapter",
			mutate: func(_ *Tree, p *AdapterSessionPayload) {
				p.SessionID = AdapterSessionID("codex", p.SessionRef)
			},
			want: "session_id does not match adapter/session_ref",
		},
		{
			name:   "unknown node",
			mutate: func(_ *Tree, p *AdapterSessionPayload) { p.NodeID = "ghost" },
			want:   "not found",
		},
		{
			name: "tombstoned node",
			mutate: func(tree *Tree, _ *AdapterSessionPayload) {
				tree.Nodes["n1"].Deleted = true
			},
			want: "tombstoned",
		},
		{
			name: "existing mapping mismatch",
			mutate: func(tree *Tree, p *AdapterSessionPayload) {
				tree.Sessions[p.SessionID] = Session{
					ID: p.SessionID, SessionRef: p.SessionRef, Adapter: "codex", NodeID: p.NodeID,
				}
			},
			want: "mapping mismatch",
		},
		{
			name: "already ended",
			mutate: func(tree *Tree, p *AdapterSessionPayload) {
				tree.Sessions[p.SessionID] = Session{
					ID: p.SessionID, SessionRef: p.SessionRef, Adapter: p.Adapter, NodeID: p.NodeID, Ended: true,
				}
			},
			ending: true,
			want:   "is already ended",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newSessionTree()
			p := baseAdapterPayload()
			tc.mutate(tree, &p)
			err := validateAdapterSessionPayload(tree, Event{EventID: "evt"}, p, tc.ending)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
