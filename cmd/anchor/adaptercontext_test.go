package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func contextFixture(t *testing.T) (root, proj string) {
	root, proj = treeFixture(t)
	return root, proj
}

func TestCLIAdapterContextHookJSON(t *testing.T) {
	root, proj := contextFixture(t)
	hook := `{"hook_event_name":"SessionStart","session_id":"sess-1","cwd":"/tmp/project","source":"startup"}`
	out, stderr, code := runCLIInput(t, root, hook, "adapter", "context", "--dir", proj, "--stdin")
	if code != 0 {
		t.Fatalf("adapter context: %s%s", out, stderr)
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("hook JSON invalid: %v (%s)", err, out)
	}
	if payload.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q", payload.HookSpecificOutput.HookEventName)
	}
	ctx := payload.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"# Anchor session context", "workspace", "Ship rate limiter", "no dropped requests",
		"Whole-interval rounding", "hidden tests expect boundaries", "Implement WaitTime", "anchor tree"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context missing %q:\n%s", want, ctx)
		}
	}
}

func TestCLIAdapterContextTruncates(t *testing.T) {
	root, proj := contextFixture(t)
	hook := `{"session_id":"s","cwd":"/tmp"}`
	out, stderr, code := runCLIInput(t, root, hook, "adapter", "context", "--dir", proj, "--stdin", "--max-bytes", "120")
	if code != 0 {
		t.Fatalf("adapter context: %s%s", out, stderr)
	}
	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatal(err)
	}
	ctx := payload.HookSpecificOutput.AdditionalContext
	if len(ctx) > 400 {
		t.Fatalf("context not truncated: %d bytes", len(ctx))
	}
	if !strings.Contains(ctx, "truncated") {
		t.Fatalf("truncation marker missing:\n%s", ctx)
	}
}

func TestCLIAdapterContextPrintAndInvalidInput(t *testing.T) {
	root, proj := contextFixture(t)
	out, stderr, code := runCLIInput(t, root, `{}`, "adapter", "context", "--dir", proj, "--stdin", "--print")
	if code != 0 {
		t.Fatalf("--print: %s%s", out, stderr)
	}
	if !strings.HasPrefix(out, "# Anchor session context") || strings.Contains(out, "hookSpecificOutput") {
		t.Fatalf("--print output wrong: %s", out)
	}
	if _, stderr, code := runCLIInput(t, root, "not-json", "adapter", "context", "--dir", proj, "--stdin"); code != 1 {
		t.Fatalf("invalid hook input accepted: %s", stderr)
	}
	if _, stderr, code := runCLIInput(t, root, "{}", "adapter", "context", "--dir", proj); code != 1 {
		t.Fatalf("missing input flags accepted: %s", stderr)
	}
}

func TestCLIAdapterContextActiveNode(t *testing.T) {
	root, proj := contextFixture(t)
	if _, stderr, code := runCLI(t, root, "node", "transition", "--dir", proj, "--id", "task1", "--to", "in_progress"); code != 0 {
		t.Fatalf("transition: %s", stderr)
	}
	out, stderr, code := runCLIInput(t, root, `{}`, "adapter", "context", "--dir", proj, "--stdin", "--print")
	if code != 0 {
		t.Fatalf("context: %s", stderr)
	}
	if !strings.Contains(out, "node: task1") || !strings.Contains(out, "Current node") {
		t.Fatalf("context did not select the in-progress task:\n%s", out)
	}
}

func TestTruncateContextKeepsRuneBoundary(t *testing.T) {
	s := strings.Repeat("界", 100)
	got := truncateContext(s, 50)
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncation marker missing: %q", got)
	}
	if strings.Contains(got, "\uFFFD") {
		t.Fatal("truncation split a rune")
	}
	if truncateContext("short", 100) != "short" {
		t.Fatal("short context changed")
	}
}

var _ = os.Stat
var _ = filepath.Join
