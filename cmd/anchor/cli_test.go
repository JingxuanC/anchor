package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var anchorBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "anchor-cli-test-*")
	if err != nil {
		panic(err)
	}
	anchorBin = filepath.Join(dir, "anchor")
	build := exec.Command("go", "build", "-o", anchorBin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build: %v: %s", err, out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func runCLI(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(anchorBin, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Anchor Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "add", "tracked.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cmd = exec.Command("git", "-C", repo, "commit", "-qm", "initial")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return repo
}

func TestCLIWorkflowRecoveryAndRestoreBoundary(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	must := func(out string, errText string, code int) string {
		if code != 0 {
			t.Fatalf("command failed code=%d stderr=%s stdout=%s", code, errText, out)
		}
		return out
	}
	must(runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"))
	must(runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "Ship"))
	must(runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "Core"))
	must(runCLI(t, root, "node", "create", "--dir", ws, "--id", "note1", "--type", "note", "--parent", "goal1", "--title", "note"))
	must(runCLI(t, root, "note", "set", "--dir", ws, "--id", "note1", "--text", "hello"))
	must(runCLI(t, root, "node", "edit", "--dir", ws, "--id", "task1", "--title", "Core v2"))
	must(runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"))
	out := must(runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json"))
	var before map[string]any
	if err := json.Unmarshal([]byte(out), &before); err != nil {
		t.Fatal(err)
	}
	if before["projection_status"] != "complete" {
		t.Fatalf("projection=%v", before["projection_status"])
	}
	if before["recovery_status"] != "incomplete" {
		t.Fatalf("unprepared recovery must be incomplete: %v", before["recovery_status"])
	}
	if !strings.Contains(out, "no prepared handoff") {
		t.Fatalf("missing reason: %s", out)
	}

	prep := must(runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo))
	var p map[string]any
	if err := json.Unmarshal([]byte(prep), &p); err != nil {
		t.Fatal(err)
	}
	if p["recovery_status"] != "complete" {
		t.Fatalf("clean prepare=%v", p["recovery_status"])
	}
	packet := p["path"].(string)
	out = must(runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json"))
	if !strings.Contains(out, `"recovery_status": "complete"`) {
		t.Fatalf("prepared clean status=%s", out)
	}

	// Drift after prepare must be stale_code, and restore must not overwrite.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = must(runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json"))
	if !strings.Contains(out, `"recovery_status": "stale_code"`) {
		t.Fatalf("drift status=%s", out)
	}
	beforeFile, _ := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	out, _, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", packet)
	if code == 0 {
		t.Fatalf("stale restore must exit nonzero: %s", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Fatalf("restore must report apply disabled: %s", out)
	}
	afterFile, _ := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if string(beforeFile) != string(afterFile) {
		t.Fatal("restore modified code")
	}
}

func TestCLIRejectsIllegalTransitions(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if out, errText, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("%s %s", out, errText)
	}
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G"); code != 0 {
		t.Fatalf("%s %s", out, errText)
	}
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T"); code != 0 {
		t.Fatalf("%s %s", out, errText)
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done"); code == 0 {
		t.Fatal("todo->done should fail")
	}
	if _, _, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "note1", "--type", "note", "--parent", "goal1", "--title", "N"); code != 0 {
		t.Fatal("note create failed")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "note1", "--to", "archived"); code != 0 {
		t.Fatal("note active->archived should pass")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "note1", "--to", "active"); code == 0 {
		t.Fatal("note archived->active should fail")
	}
}

func TestCLIConsecutiveTransitions(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if out, errText, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("%s %s", out, errText)
	}
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G"); code != 0 {
		t.Fatalf("%s %s", out, errText)
	}
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T"); code != 0 {
		t.Fatalf("%s %s", out, errText)
	}
	sha := strings.Repeat("a", 64)
	if out, errText, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--sha256", sha); code != 0 {
		t.Fatalf("evidence add: %s %s", out, errText)
	}
	for _, to := range []string{"in_progress", "done"} {
		args := []string{"node", "transition", "--dir", ws, "--id", "task1", "--to", to}
		if to == "done" {
			args = append(args, "--evidence", "ev1")
		}
		if out, errText, code := runCLI(t, root, args...); code != 0 {
			t.Fatalf("transition %s: %s %s", to, out, errText)
		}
	}
	out, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(out, `"projection_status": "complete"`) {
		t.Fatalf("projection incomplete after consecutive transitions: %s", out)
	}
	if !strings.Contains(out, `"status": "done"`) {
		t.Fatalf("final status not done: %s", out)
	}
}

func TestCLIDoneRequiresEvidenceOrLocalOverride(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"); code != 0 {
		t.Fatal("in_progress failed")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done"); code == 0 {
		t.Fatal("done without evidence should fail")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done", "--user-override"); code == 0 {
		t.Fatal("user_override must fail closed")
	}
	sha := strings.Repeat("b", 64)
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--sha256", sha); code != 0 {
		t.Fatal("evidence add failed")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done", "--evidence", "ev1"); code != 0 {
		t.Fatal("done with registered evidence should pass")
	}
}

func TestCLIRejectsNonGoalWithoutParent(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	if _, _, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--title", "T"); code == 0 {
		t.Fatal("non-goal without parent exited 0")
	}
	out, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if strings.Contains(out, `"task1"`) {
		t.Fatalf("invalid node was written: %s", out)
	}
}

func TestCLITombstoneRejectsLaterMutation(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	if _, _, code := runCLI(t, root, "node", "tombstone", "--dir", ws, "--id", "task1"); code != 0 {
		t.Fatal("tombstone failed")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"); code == 0 {
		t.Fatal("transition after tombstone exited 0")
	}
	if _, _, code := runCLI(t, root, "node", "edit", "--dir", ws, "--id", "task1", "--title", "X"); code == 0 {
		t.Fatal("edit after tombstone exited 0")
	}
}
