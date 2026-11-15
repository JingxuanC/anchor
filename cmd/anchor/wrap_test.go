package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wrapFixture(t *testing.T) (root, ws, repo string) {
	t.Helper()
	root = t.TempDir()
	ws = filepath.Join(root, "ws")
	repo = initRepo(t)
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G", "--north-star", "ship it"); code != 0 {
		t.Fatalf("goal: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "do work"); code != 0 {
		t.Fatalf("task: %s", stderr)
	}
	return root, ws, repo
}

func TestCLIWrapRunsChildAndRecordsSession(t *testing.T) {
	root, ws, _ := wrapFixture(t)
	script := `printf 'node=%s\nws=%s\nsess=%s\nctx=%s\n' "$ANCHOR_NODE" "$ANCHOR_WORKSPACE" "$ANCHOR_SESSION" "$ANCHOR_CONTEXT_FILE"; exit 3`
	out, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--node", "task1", "--json", "--", "sh", "-c", script)
	if code != 3 {
		t.Fatalf("wrap exit = %d, want child exit 3 (stderr=%s)", code, stderr)
	}
	canonical, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"node=task1", "ws=" + canonical} {
		if !strings.Contains(out, want) {
			t.Fatalf("child stdout missing %q: %s", want, out)
		}
	}
	records, err := filepath.Glob(filepath.Join(ws, "local", "wrap", "*.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("wrap record missing: %v %v", records, err)
	}
	var rec struct {
		SessionID   string   `json:"session_id"`
		NodeID      string   `json:"node_id"`
		ExitCode    int      `json:"exit_code"`
		ContextFile string   `json:"context_file"`
		Command     []string `json:"command"`
	}
	b, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.ExitCode != 3 || rec.NodeID != "task1" || rec.SessionID == "" || len(rec.Command) == 0 {
		t.Fatalf("wrap record wrong: %s", b)
	}
	ctx, err := os.ReadFile(rec.ContextFile)
	if err != nil {
		t.Fatalf("context file missing: %v", err)
	}
	for _, want := range []string{"node: task1", "north star: ship it", "do work", "handoff prepare"} {
		if !strings.Contains(string(ctx), want) {
			t.Fatalf("context file missing %q:\n%s", want, ctx)
		}
	}
	// The session is projected as an ended wrapped session.
	statusOut, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status: %s", statusOut)
	}
	var status struct {
		Tree struct {
			Sessions map[string]struct {
				Adapter string `json:"adapter"`
				NodeID  string `json:"node_id"`
				Ended   bool   `json:"ended"`
			} `json:"sessions"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatal(err)
	}
	sess, ok := status.Tree.Sessions[rec.SessionID]
	if !ok || sess.Adapter != "wrap" || sess.NodeID != "task1" || !sess.Ended {
		t.Fatalf("wrapped session not projected: %s", statusOut)
	}
}

func TestCLIWrapDoesNotInvokeAShell(t *testing.T) {
	root, ws, _ := wrapFixture(t)
	argsFile := filepath.Join(root, "args.txt")
	script := filepath.Join(root, "record.sh")
	content := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "pwned")
	wantArgs := []string{"a b", "$(touch " + marker + ")", "c;d"}
	_, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--node", "task1", "--", script, wantArgs[0], wantArgs[1], wantArgs[2])
	if code != 0 {
		t.Fatalf("wrap: %s", stderr)
	}
	// The child receives argv verbatim: no word splitting, no command substitution.
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("child did not record its argv: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != len(wantArgs) {
		t.Fatalf("argv = %q, want %q", lines, wantArgs)
	}
	for i := range wantArgs {
		if lines[i] != wantArgs[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, lines[i], wantArgs[i])
		}
	}
	if strings.Contains(string(got), marker) {
		_ = os.Remove(marker)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("wrap executed the child through a shell (metacharacter injection)")
	}
}

func TestCLIWrapRequiresLiveNode(t *testing.T) {
	root, ws, _ := wrapFixture(t)
	marker := filepath.Join(root, "ran")
	if _, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--node", "missing", "--", "sh", "-c", "touch "+marker); code != 1 {
		t.Fatalf("wrap with a missing node accepted: %s", stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("wrap ran the child despite a missing node")
	}
	if _, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--", "sh", "-c", "true"); code != 1 {
		t.Fatalf("wrap without --node accepted: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--node", "task1"); code != 1 {
		t.Fatalf("wrap without a command accepted: %s", stderr)
	}
}

func TestCLIWrapPrepareHandoffArmsGate(t *testing.T) {
	root, ws, repo := wrapFixture(t)
	out, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--node", "task1", "--repo", repo, "--prepare-handoff", "--json", "--", "sh", "-c", "exit 0")
	if code != 0 {
		t.Fatalf("wrap --prepare-handoff: %s%s", out, stderr)
	}
	if !strings.Contains(stderr, `"handoff_id"`) {
		t.Fatalf("wrap summary has no handoff: %s", stderr)
	}
	// The prepared handoff arms the successor gate.
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "t2", "--type", "task", "--parent", "goal1", "--title", "blocked"); code != 1 {
		t.Fatalf("wrap --prepare-handoff did not arm the gate: %s", stderr)
	}
	// Wrapping is still possible while the gate is pending (read-only session).
	if _, stderr, code := runCLI(t, root, "wrap", "--dir", ws, "--node", "task1", "--", "sh", "-c", "exit 0"); code != 0 {
		t.Fatalf("wrap refused while the gate is pending: %s", stderr)
	}
}
