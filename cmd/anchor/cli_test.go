package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
)

var anchorBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "anchor-cli-test-*")
	if err != nil {
		panic(err)
	}
	anchorBin = filepath.Join(dir, "anchor")
	build := exec.Command("go", "build", "-tags", "anchor_test_hooks", "-o", anchorBin, ".")
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
	evFile := writeFixture(t, filepath.Join(root, "evidence.txt"), "clean test fixture\n")
	if out, errText, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", evFile); code != 0 {
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
	evFile := writeFixture(t, filepath.Join(root, "evidence.txt"), "clean test fixture\n")
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", evFile); code != 0 {
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

type cliProc struct {
	cmd    *exec.Cmd
	stdout strings.Builder
	stderr strings.Builder
	code   int
}

func startCLIAtBarrier(t *testing.T, dir, readyDir, barrier string, args ...string) *cliProc {
	t.Helper()
	script := `touch "$ANCHOR_READY/ready.$$"; while [ ! -e "$ANCHOR_BARRIER" ]; do sleep 0.002; done; exec "$ANCHOR_BIN" "$@"`
	p := &cliProc{}
	cmdArgs := append([]string{"-c", script, "anchor"}, args...)
	p.cmd = exec.Command("sh", cmdArgs...)
	p.cmd.Dir = dir
	p.cmd.Env = append(os.Environ(), "ANCHOR_READY="+readyDir, "ANCHOR_BARRIER="+barrier, "ANCHOR_BIN="+anchorBin)
	p.cmd.Stdout = &p.stdout
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return p
}

func raceCLI(t *testing.T, dir string, specs ...[]string) []*cliProc {
	t.Helper()
	readyDir := t.TempDir()
	barrier := filepath.Join(t.TempDir(), "barrier")
	procs := make([]*cliProc, 0, len(specs))
	for _, spec := range specs {
		procs = append(procs, startCLIAtBarrier(t, dir, readyDir, barrier, spec...))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(readyDir)
		if len(entries) >= len(specs) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes did not reach barrier: %d/%d", len(entries), len(specs))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		err := p.cmd.Wait()
		if err == nil {
			p.code = 0
		} else if exit, ok := err.(*exec.ExitError); ok {
			p.code = exit.ExitCode()
		} else {
			t.Fatalf("wait: %v", err)
		}
	}
	return procs
}

func eventFiles(t *testing.T, ws string) []core.Event {
	t.Helper()
	s, err := core.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	return r.Events
}

func writeFixture(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIConcurrentSameNodeOneWins(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	before := len(eventFiles(t, ws))
	procs := raceCLI(t, root,
		[]string{"node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"},
		[]string{"node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"},
	)
	success, failed := 0, 0
	for _, p := range procs {
		if p.code == 0 {
			success++
		} else {
			failed++
			if p.stderr.Len() == 0 {
				t.Fatalf("failing process had no diagnostic: stdout=%s", p.stdout.String())
			}
			if !strings.Contains(p.stderr.String(), "illegal transition") && !strings.Contains(p.stderr.String(), "expected_revision") && !strings.Contains(p.stderr.String(), "revision conflict") {
				t.Fatalf("failure was not a clear stale/illegal rejection: %s", p.stderr.String())
			}
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("expected one winner and one loser, got success=%d failed=%d: %+v", success, failed, procs)
	}
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status failed: %s", out)
	}
	var st struct {
		Projection string `json:"projection_status"`
		Conflicts  []any  `json:"conflicts"`
		Tree       struct {
			Nodes map[string]struct {
				Status   string `json:"status"`
				Revision int    `json:"revision"`
			} `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st.Projection != "complete" || len(st.Conflicts) != 0 {
		t.Fatalf("projection was corrupted by concurrent CLI: %s", out)
	}
	if n := st.Tree.Nodes["task1"]; n.Status != "in_progress" || n.Revision != 2 {
		t.Fatalf("winning transition not retained: %+v", n)
	}
	events := eventFiles(t, ws)
	if len(events) != before+1 {
		t.Fatalf("expected exactly one new event, before=%d after=%d", before, len(events))
	}
	statusEvents := 0
	for _, e := range events {
		if e.Type == core.EvStatusChanged {
			statusEvents++
		}
	}
	if statusEvents != 1 {
		t.Fatalf("expected exactly one status_changed event, got %d", statusEvents)
	}
}

func TestCLIConcurrentIndependentNodesBothWin(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "taskA", "--type", "task", "--parent", "goal1", "--title", "A")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "taskB", "--type", "task", "--parent", "goal1", "--title", "B")
	before := len(eventFiles(t, ws))
	procs := raceCLI(t, root,
		[]string{"node", "transition", "--dir", ws, "--id", "taskA", "--to", "in_progress"},
		[]string{"node", "transition", "--dir", ws, "--id", "taskB", "--to", "in_progress"},
	)
	for _, p := range procs {
		if p.code != 0 {
			t.Fatalf("independent-node transition failed: %s%s", p.stdout.String(), p.stderr.String())
		}
	}
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status failed: %s", out)
	}
	if !strings.Contains(out, `"projection_status": "complete"`) || !strings.Contains(out, `"conflicts": []`) {
		t.Fatalf("independent transitions did not both apply cleanly: %s", out)
	}
	for _, id := range []string{"taskA", "taskB"} {
		if !strings.Contains(out, `"id": "`+id+`"`) || !strings.Contains(out, `"status": "in_progress"`) {
			t.Fatalf("missing in_progress node %s: %s", id, out)
		}
	}
	events := eventFiles(t, ws)
	if len(events) != before+2 {
		t.Fatalf("expected two new events, before=%d after=%d", before, len(events))
	}
	seqs := map[uint64]bool{}
	statusEvents := 0
	for _, e := range events {
		if e.Type == core.EvStatusChanged {
			statusEvents++
		}
		if e.DeviceSeq == 0 {
			t.Fatalf("missing device_seq: %+v", e)
		}
		if seqs[e.DeviceSeq] {
			t.Fatalf("duplicate device_seq %d after concurrent independent writes", e.DeviceSeq)
		}
		seqs[e.DeviceSeq] = true
	}
	if statusEvents != 2 {
		t.Fatalf("expected two status_changed events, got %d", statusEvents)
	}
}

func TestCLIRawAppendCannotBypassTransactionValidation(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	before := len(eventFiles(t, ws))
	raw := filepath.Join(root, "raw.json")
	noChain := core.Event{
		SchemaVersion: core.SchemaVersion,
		Type:          core.EvStatusChanged,
		CausalParents: []string{"not-the-latest-node-event"},
		BaseRevision:  1, ExpectedRevision: 2,
		LeaseID: "lease_task1", FencingToken: 1,
		Payload: json.RawMessage(`{"node_id":"task1","status":"in_progress"}`),
	}
	b, _ := json.Marshal(noChain)
	if err := os.WriteFile(raw, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", raw); code == 0 {
		t.Fatalf("raw append without causal chain exited 0: %s", stderr)
	}
	if len(eventFiles(t, ws)) != before {
		t.Fatal("rejected raw append wrote an event")
	}
	out, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	var st struct {
		Tree struct {
			Nodes map[string]struct {
				LastEventID  string `json:"last_event_id"`
				LeaseEventID string `json:"lease_event_id"`
				Revision     int    `json:"revision"`
			} `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	n := st.Tree.Nodes["task1"]
	zero := core.Event{
		SchemaVersion: core.SchemaVersion,
		Type:          core.EvStatusChanged,
		CausalParents: []string{n.LastEventID, n.LeaseEventID},
		BaseRevision:  n.Revision, ExpectedRevision: 0,
		LeaseID: "lease_task1", FencingToken: 1,
		Payload: json.RawMessage(`{"node_id":"task1","status":"in_progress"}`),
	}
	b, _ = json.Marshal(zero)
	if err := os.WriteFile(raw, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", raw); code == 0 {
		t.Fatalf("raw append with expected_revision=0 exited 0: %s", stderr)
	}
	if len(eventFiles(t, ws)) != before {
		t.Fatal("zero expected_revision append wrote an event")
	}
}

func TestCLITransactionCrashBetweenEventsNotVisible(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	ready := filepath.Join(root, "txn-ready")
	cmd := exec.Command(anchorBin, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ANCHOR_TEST_TXN_STOP_AFTER_EVENT=1", "ANCHOR_TEST_TXN_READY="+ready)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("transaction did not pause after first event: %s%s", stdout.String(), stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()

	if events := eventFiles(t, ws); len(events) != 0 {
		t.Fatalf("incomplete transaction became visible: %d events", len(events))
	}
	tmps, err := filepath.Glob(filepath.Join(ws, "events", ".txn-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmps) == 0 {
		t.Fatal("expected an uncommitted temp batch after the kill")
	}
	// Recovery: a fresh transaction sees only committed batches and succeeds.
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G"); code != 0 {
		t.Fatalf("recovery create failed: %s %s", out, errText)
	}
	if events := eventFiles(t, ws); len(events) != 2 {
		t.Fatalf("recovered transaction should commit both events, got %d", len(events))
	}
	if tmps, _ := filepath.Glob(filepath.Join(ws, "events", ".txn-*.tmp")); len(tmps) != 0 {
		t.Fatalf("stale transaction temp was not cleaned during recovery: %v", tmps)
	}
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 || !strings.Contains(out, `"projection_status": "complete"`) || !strings.Contains(out, `"goal1"`) {
		t.Fatalf("recovery projection invalid: %s", out)
	}
}

func TestCLIEvidenceArtifactSecurityAndDoneGate(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	data := "verified local fixture\n"
	file := writeFixture(t, filepath.Join(root, "artifact.txt"), data)
	sum := sha256.Sum256([]byte(data))
	digest := hex.EncodeToString(sum[:])
	if out, errText, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", file, "--sha256", digest); code != 0 {
		t.Fatalf("evidence add failed: %s %s", out, errText)
	}
	blob := filepath.Join(ws, "evidence", "blobs", digest)
	orphan := strings.Repeat("b", 64)
	orphanPath := filepath.Join(ws, "evidence", "blobs", orphan)
	if err := os.WriteFile(orphanPath, []byte("unreferenced fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auditOut, _, code := runCLI(t, root, "evidence", "audit", "--dir", ws, "--json")
	if code != 0 || !strings.Contains(auditOut, `"read_only": true`) || !strings.Contains(auditOut, `"gc_performed": false`) || !strings.Contains(auditOut, orphan) {
		t.Fatalf("evidence audit failed: %s", auditOut)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatal("audit deleted the orphan blob")
	}
	info, err := os.Stat(blob)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode = %o, want 600", info.Mode().Perm())
	}
	before := len(eventFiles(t, ws))
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-bad", "--node", "task1", "--file", file, "--sha256", strings.Repeat("0", 64)); code == 0 {
		t.Fatal("wrong expected sha256 accepted")
	}
	secret := writeFixture(t, filepath.Join(root, "secret.txt"), "token=ghp_1234567890abcdefghijklmnopqrstuvwx\n")
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-secret", "--node", "task1", "--file", secret); code == 0 {
		t.Fatal("secret content accepted")
	}
	secretName := writeFixture(t, filepath.Join(root, "AKIAIOSFODNN7EXAMPLE.txt"), "clean\n")
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-name", "--node", "task1", "--file", secretName); code == 0 {
		t.Fatal("secret filename accepted")
	}
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-meta", "--node", "task1", "--kind", "password = hunter2hunter2", "--file", file); code == 0 {
		t.Fatal("secret metadata accepted")
	}
	link := filepath.Join(root, "artifact-link.txt")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-link", "--node", "task1", "--file", link); code == 0 {
		t.Fatal("symlink artifact accepted")
	}
	if len(eventFiles(t, ws)) != before {
		t.Fatal("rejected evidence add wrote an event")
	}
	// Secret bytes must not have been stored under a guessed digest.
	if _, err := os.Stat(filepath.Join(ws, "evidence", "blobs", "not-a-digest")); err == nil {
		t.Fatal("unexpected artifact path")
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"); code != 0 {
		t.Fatal("in_progress failed")
	}
	if out, errText, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done", "--evidence", "ev1"); code != 0 {
		t.Fatalf("done with persisted evidence failed: %s %s", out, errText)
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status failed: %s", out)
	}
	if !strings.Contains(out, `"projection_status": "incomplete"`) || !strings.Contains(out, "missing") {
		t.Fatalf("missing artifact did not invalidate projection: %s", out)
	}
}

func TestProductionBuildHasNoTestPauseHook(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "anchor-prod")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("production build failed: %v: %s", err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("ANCHOR_TEST_TXN_STOP_AFTER_EVENT")) {
		t.Fatal("production binary contains the test pause hook")
	}
}

func TestCLIEvidenceCannotCrossNodeOrUseTamperedBytes(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	goalFile := writeFixture(t, filepath.Join(root, "goal-evidence.txt"), "goal fixture\n")
	if out, errText, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-goal", "--node", "goal1", "--kind", "test", "--file", goalFile); code != 0 {
		t.Fatalf("goal evidence add failed: %s %s", out, errText)
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"); code != 0 {
		t.Fatal("in_progress failed")
	}
	beforeDone := len(eventFiles(t, ws))
	if _, stderr, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done", "--evidence", "ev-goal"); code == 0 {
		t.Fatalf("cross-node evidence accepted: %s", stderr)
	} else if !strings.Contains(stderr, "belongs to node goal1") {
		t.Fatalf("cross-node failure lacked ownership diagnostic: %s", stderr)
	}
	if len(eventFiles(t, ws)) != beforeDone {
		t.Fatal("cross-node done wrote an event")
	}
	taskFile := writeFixture(t, filepath.Join(root, "task-evidence.txt"), "task fixture\n")
	if out, errText, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev-task", "--node", "task1", "--kind", "test", "--file", taskFile); code != 0 {
		t.Fatalf("task evidence add failed: %s %s", out, errText)
	}
	sum := sha256.Sum256([]byte("task fixture\n"))
	blob := filepath.Join(ws, "evidence", "blobs", hex.EncodeToString(sum[:]))
	if err := os.WriteFile(blob, []byte("tampered local bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeTamper := len(eventFiles(t, ws))
	if _, stderr, code := runCLI(t, root, "node", "transition", "--dir", ws, "--id", "task1", "--to", "done", "--evidence", "ev-task"); code == 0 {
		t.Fatalf("tampered evidence accepted: %s", stderr)
	} else if !strings.Contains(stderr, "artifact failed verification") && !strings.Contains(stderr, "mismatch") {
		t.Fatalf("tamper failure lacked diagnostic: %s", stderr)
	}
	if len(eventFiles(t, ws)) != beforeTamper {
		t.Fatal("tampered done wrote an event")
	}
	out, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(out, `"status": "in_progress"`) || !strings.Contains(out, `"projection_status": "incomplete"`) {
		t.Fatalf("tampered evidence did not invalidate projection: %s", out)
	}
}

func runCLIInput(t *testing.T, dir, input string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(anchorBin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
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

func TestCLIAdapterDryRunCaptureAndNoSharedInstall(t *testing.T) {
	root := t.TempDir()
	out, stderr, code := runCLI(t, root, "adapter", "capabilities", "--json")
	if code != 0 || !strings.Contains(out, `"claude-code"`) || !strings.Contains(out, `"verified_native_hooks": false`) {
		t.Fatalf("capabilities failed: %s%s", out, stderr)
	}
	out, stderr, code = runCLI(t, root, "adapter", "render", "--adapter", "claude-code", "--print")
	if code != 0 || !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"native_hook_status": "unverified"`) || !strings.Contains(out, `"installation": "manual_review_only"`) {
		t.Fatalf("render print failed: %s%s", out, stderr)
	}
	target := filepath.Join(root, "render")
	out, stderr, code = runCLI(t, root, "adapter", "render", "--adapter", "claude-code", "--target", target)
	if code != 0 || !strings.Contains(out, `"installed": false`) {
		t.Fatalf("render target failed: %s%s", out, stderr)
	}
	for _, p := range []string{"render/claude-code/anchor-adapter.json", "render/claude-code/settings.fragment.json"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("missing rendered file %s: %v", p, err)
		}
	}
	if _, _, code := runCLI(t, root, "adapter", "render", "--adapter", "claude-code", "--target", target); code == 0 {
		t.Fatal("second render overwrote existing target files")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, _, code := runCLI(t, root, "adapter", "render", "--adapter", "claude-code", "--target", filepath.Join(home, ".claude")); code == 0 {
		t.Fatal("shared ~/.claude render accepted")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Fatal("shared config path was created")
	}

	event := writeFixture(t, filepath.Join(root, "hook.json"), `{"hook_event_name":"PostToolUse","session_id":"raw-session-secret-id","cwd":"/tmp/fixture/project","tool_name":"Bash","timestamp":"2026-11-15T00:00:00Z","prompt":"must-not-be-copied","tool_input":{"command":"echo fixture"}}`)
	out, stderr, code = runCLI(t, root, "adapter", "capture", "--adapter", "claude-code", "--event", event, "--print")
	if code != 0 {
		t.Fatalf("capture print failed: %s%s", out, stderr)
	}
	if !strings.Contains(out, `"session_ref": "sha256:`) || strings.Contains(out, "raw-session-secret-id") || strings.Contains(out, "must-not-be-copied") {
		t.Fatalf("capture leaked or failed to hash metadata: %s", out)
	}
	if !strings.Contains(out, `"native_hook_verified": false`) || !strings.Contains(out, `"does_not_restore_or_block": true`) {
		t.Fatalf("capture overclaimed behavior: %s", out)
	}
	stdinOut, stderr, code := runCLIInput(t, root, `{"hook_event_name":"PostToolUse","session_id":"stdin-session","tool_name":"Read"}`, "adapter", "capture", "--adapter", "claude-code", "--stdin", "--print")
	if code != 0 || !strings.Contains(stdinOut, `"tool": "Read"`) {
		t.Fatalf("stdin capture failed: %s%s", stdinOut, stderr)
	}
	captureOut := filepath.Join(root, "capture.json")
	if _, stderr, code := runCLI(t, root, "adapter", "capture", "--adapter", "claude-code", "--event", event, "--out", captureOut); code != 0 {
		t.Fatalf("capture --out failed: %s", stderr)
	}
	if _, _, code := runCLI(t, root, "adapter", "capture", "--adapter", "claude-code", "--event", event, "--out", captureOut); code == 0 {
		t.Fatal("capture --out overwrote an existing file")
	}
	secretEvent := writeFixture(t, filepath.Join(root, "secret-hook.json"), `{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash","prompt":"AKIAIOSFODNN7EXAMPLE"}`)
	if _, stderr, code := runCLI(t, root, "adapter", "capture", "--adapter", "claude-code", "--event", secretEvent, "--print"); code == 0 {
		t.Fatal("secret-bearing capture accepted")
	} else if strings.Contains(stderr, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("capture error leaked the secret")
	}
}

func assertNoRawCaptureData(t *testing.T, root string, needles ...string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range needles {
			if bytes.Contains(b, []byte(needle)) {
				t.Fatalf("raw capture value %q persisted in %s", needle, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCLIAdapterIngestSessionMappingIdempotencyAndSafety(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	fixture := writeFixture(t, filepath.Join(root, "hook.json"), `{"hook_event_name":"PostToolUse","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","tool_name":"Bash","timestamp":"2026-11-15T00:00:00Z","source_event_id":"source-event-1","source_seq":1,"prompt":"must-not-be-copied","tool_input":{"command":"echo must-not-be-copied"}}`)
	before := len(eventFiles(t, ws))
	out, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", fixture, "--source", "fixture", "--json")
	if code != 0 {
		t.Fatalf("ingest failed: %s%s", out, stderr)
	}
	var ing map[string]any
	if err := json.Unmarshal([]byte(out), &ing); err != nil {
		t.Fatal(err)
	}
	sessionID, _ := ing["session_id"].(string)
	if sessionID == "" || ing["event_type"] != "adapter_session_seen" || ing["created"] != true || ing["session_end_is_task_done"] != false || ing["auto_install"] != false || ing["upload"] != false {
		t.Fatalf("unexpected ingest result: %s", out)
	}
	if len(eventFiles(t, ws)) != before+1 {
		t.Fatalf("ingest event count=%d want %d", len(eventFiles(t, ws)), before+1)
	}
	statusOut, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status failed: %s", statusOut)
	}
	var st struct {
		Projection string `json:"projection_status"`
		Tree       struct {
			Nodes map[string]struct {
				Status string `json:"status"`
			} `json:"nodes"`
			Sessions map[string]struct {
				Adapter    string `json:"adapter"`
				NodeID     string `json:"node_id"`
				Source     string `json:"source"`
				TrustLevel string `json:"trust_level"`
				Ended      bool   `json:"ended"`
			} `json:"sessions"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil {
		t.Fatal(err)
	}
	if st.Projection != "complete" || len(st.Tree.Nodes) != 2 || len(st.Tree.Sessions) != 1 {
		t.Fatalf("session mapping/tree invalid: %s", statusOut)
	}
	sess := st.Tree.Sessions[sessionID]
	if sess.Adapter != "claude-code" || sess.NodeID != "task1" || sess.Source != "fixture" || sess.TrustLevel != "unverified_adapter" || sess.Ended {
		t.Fatalf("session mapping wrong: %+v", sess)
	}
	if st.Tree.Nodes["task1"].Status != "todo" {
		t.Fatalf("ingest changed task status: %+v", st.Tree.Nodes["task1"])
	}
	assertNoRawCaptureData(t, ws, "raw-session-id", "/tmp/raw-cwd/project", "must-not-be-copied", "echo must-not-be-copied")

	// Two processes ingest the same fixture: one event, one session, no new node.
	beforeDup := len(eventFiles(t, ws))
	procs := raceCLI(t, root,
		[]string{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", fixture, "--source", "fixture", "--json"},
		[]string{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", fixture, "--source", "fixture", "--json"},
	)
	for _, p := range procs {
		if p.code != 0 {
			t.Fatalf("duplicate ingest failed: %s%s", p.stdout.String(), p.stderr.String())
		}
	}
	if len(eventFiles(t, ws)) != beforeDup {
		t.Fatalf("duplicate ingest wrote an event: before=%d after=%d", beforeDup, len(eventFiles(t, ws)))
	}
	statusOut, _, _ = runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(statusOut, `"sessions"`) || !strings.Contains(statusOut, sessionID) || strings.Count(statusOut, `"adapter": "claude-code"`) != 1 {
		t.Fatalf("duplicate ingest created a second session: %s", statusOut)
	}

	// Out-of-order observations with different timestamps both apply to one session.
	event2 := writeFixture(t, filepath.Join(root, "hook2.json"), `{"hook_event_name":"PostToolUse","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","tool_name":"Read","timestamp":"2026-11-15T00:00:02Z","source_event_id":"source-event-2","source_seq":2}`)
	event3 := writeFixture(t, filepath.Join(root, "hook3.json"), `{"hook_event_name":"PreToolUse","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","tool_name":"Grep","timestamp":"2026-11-14T00:00:00Z","source_event_id":"source-event-3","source_seq":1}`)
	for _, f := range []string{event2, event3} {
		if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", f, "--source", "fixture"); code != 0 {
			t.Fatalf("out-of-order ingest failed: %s", stderr)
		}
	}
	statusOut, _, _ = runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(statusOut, `"projection_status": "complete"`) || !strings.Contains(statusOut, `"last_hook_event": "PostToolUse"`) {
		t.Fatalf("out-of-order observations did not replay cleanly: %s", statusOut)
	}

	// SessionEnd before a session_seen is rejected.
	endUnknown := writeFixture(t, filepath.Join(root, "end-unknown.json"), `{"hook_event_name":"SessionEnd","session_id":"unknown-session","cwd":"/tmp/raw-cwd/project","timestamp":"2026-11-15T00:01:00Z","source_event_id":"source-end-unknown","source_seq":1}`)
	beforeBad := len(eventFiles(t, ws))
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", endUnknown, "--source", "fixture"); code == 0 {
		t.Fatalf("session_end before session_seen accepted: %s", stderr)
	}
	if len(eventFiles(t, ws)) != beforeBad {
		t.Fatal("rejected session_end wrote an event")
	}

	// SessionEnd is a lightweight marker: it must not mark the task done.
	end := writeFixture(t, filepath.Join(root, "end.json"), `{"hook_event_name":"SessionEnd","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","timestamp":"2026-11-15T00:03:00Z","source_event_id":"source-end-1","source_seq":3}`)
	out, stderr, code = runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", end, "--source", "fixture", "--json")
	if code != 0 || !strings.Contains(out, `"session_end_is_task_done": false`) {
		t.Fatalf("session_end ingest failed: %s%s", out, stderr)
	}
	statusOut, _, _ = runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(statusOut, `"ended": true`) || !strings.Contains(statusOut, `"status": "todo"`) {
		t.Fatalf("session_end changed task status or was not recorded: %s", statusOut)
	}
	if len(eventFiles(t, ws)) != beforeBad+1 {
		t.Fatalf("session_end event count wrong: %d", len(eventFiles(t, ws)))
	}

	// Unknown adapter, missing node, missing session_id, and secrets must not write.
	beforeReject := len(eventFiles(t, ws))
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "not-an-adapter", "--dir", ws, "--node", "task1", "--event", fixture); code == 0 {
		t.Fatal("unknown adapter accepted")
	}
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "missing-node", "--event", fixture); code == 0 {
		t.Fatal("missing mapping node accepted")
	}
	noSession := writeFixture(t, filepath.Join(root, "no-session.json"), `{"hook_event_name":"PostToolUse","tool_name":"Bash","source_event_id":"source-no-session"}`)
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", noSession); code == 0 {
		t.Fatal("missing session_id accepted")
	}
	badSource := writeFixture(t, filepath.Join(root, "bad-source.json"), `{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash","source_event_id":"source-bad-source"}`)
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", badSource, "--source", "native"); code == 0 {
		t.Fatal("unverified native source accepted")
	}
	secretFile := writeFixture(t, filepath.Join(root, "secret-hook.json"), `{"hook_event_name":"PostToolUse","session_id":"secret-session","tool_name":"Bash","source_event_id":"source-secret","prompt":"AKIAIOSFODNN7EXAMPLE"}`)
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", secretFile); code == 0 {
		t.Fatal("secret-bearing ingest accepted")
	} else if strings.Contains(stderr, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("ingest error leaked secret")
	}
	if len(eventFiles(t, ws)) != beforeReject {
		t.Fatal("a rejected ingest wrote an event")
	}
	assertNoRawCaptureData(t, ws, "AKIAIOSFODNN7EXAMPLE")
}

func TestCLIAdapterIngestNeverExecutesHookInput(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	pwn := filepath.Join(root, "hook-command-must-not-run")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	fixture := writeFixture(t, filepath.Join(root, "hook-exec.json"), `{"hook_event_name":"PostToolUse","session_id":"session-exec","cwd":"/tmp/fixture","tool_name":"Bash","source_event_id":"source-exec","source_seq":1,"tool_input":{"command":"touch `+pwn+`"}}`)
	if out, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", fixture, "--source", "fixture"); code != 0 {
		t.Fatalf("ingest failed: %s%s", out, stderr)
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("hook input command was executed")
	}
}

func TestCLIEventFormatDowngradeRejectedAndAudited(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	manifestBytes, err := os.ReadFile(filepath.Join(ws, "manifest.json"))
	if err != nil || !strings.Contains(string(manifestBytes), `"event_log_version": 2`) {
		t.Fatalf("new workspace missing persisted event_log_version=2: %s %v", manifestBytes, err)
	}
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G"); code != 0 {
		t.Fatalf("create failed: %s%s", out, errText)
	}
	paths, err := filepath.Glob(filepath.Join(ws, "events", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("batch file not found: %v %v", paths, err)
	}
	batchPath := paths[0]
	original, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		AnchorBatch  int               `json:"anchor_batch"`
		BatchID      string            `json:"batch_id"`
		EventCount   int               `json:"event_count"`
		EventsSHA256 string            `json:"events_sha256"`
		Events       []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(original, &env); err != nil {
		t.Fatal(err)
	}
	if env.AnchorBatch != 1 || len(env.Events) != 2 {
		t.Fatalf("unexpected batch: %+v", env)
	}
	status := func() map[string]any {
		t.Helper()
		out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
		if code != 0 {
			t.Fatalf("status failed: %s", out)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	assertTampered := func(reason string) {
		t.Helper()
		m := status()
		if m["projection_status"] != "incomplete" || m["recovery_status"] != "incomplete" {
			t.Fatalf("tampered batch not incomplete: %v", m)
		}
		q, _ := m["quarantined"].([]any)
		if len(q) != 1 || !strings.Contains(q[0].(map[string]any)["reason"].(string), reason) {
			t.Fatalf("quarantine reason missing %q: %v", reason, m["quarantined"])
		}
		nodes := m["tree"].(map[string]any)["nodes"].(map[string]any)
		if len(nodes) != 0 {
			t.Fatalf("tampered batch partially applied nodes: %v", nodes)
		}
	}
	bare, _ := json.Marshal([]json.RawMessage{env.Events[0]})
	if err := os.WriteFile(batchPath, bare, 0o600); err != nil {
		t.Fatal(err)
	}
	assertTampered("legacy bare event array")
	beforeAudit, _ := os.ReadFile(batchPath)
	auditOut, _, code := runCLI(t, root, "event", "audit", "--dir", ws, "--json")
	if code != 0 || !strings.Contains(auditOut, `"read_only": true`) || !strings.Contains(auditOut, `"writes": false`) || !strings.Contains(auditOut, `"migration_dry_run_only": true`) || !strings.Contains(auditOut, `"legacy_bare_array"`) || !strings.Contains(auditOut, filepath.Base(batchPath)) {
		t.Fatalf("event audit failed: %s", auditOut)
	}
	afterAudit, _ := os.ReadFile(batchPath)
	if !bytes.Equal(beforeAudit, afterAudit) {
		t.Fatal("event audit mutated the batch")
	}
	single, _ := json.Marshal(env.Events[0])
	if err := os.WriteFile(batchPath, single, 0o600); err != nil {
		t.Fatal(err)
	}
	assertTampered("legacy single-event object")
	truncated := map[string]any{
		"anchor_batch": env.AnchorBatch, "batch_id": env.BatchID,
		"event_count": env.EventCount, "events_sha256": env.EventsSHA256,
		"events": []json.RawMessage{env.Events[0]},
	}
	tb, _ := json.Marshal(truncated)
	if err := os.WriteFile(batchPath, tb, 0o600); err != nil {
		t.Fatal(err)
	}
	assertTampered("event_count mismatch")
	auditOut, _, _ = runCLI(t, root, "event", "audit", "--dir", ws, "--json")
	if !strings.Contains(auditOut, `"invalid_manifest_batch"`) || !strings.Contains(auditOut, `"invalid_files"`) {
		t.Fatalf("truncated manifest not audited: %s", auditOut)
	}
	if err := os.WriteFile(batchPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	m := status()
	if m["projection_status"] != "complete" || len(m["tree"].(map[string]any)["nodes"].(map[string]any)) != 1 {
		t.Fatalf("restored batch not readable: %v", m)
	}
}

func runCLITimeout(t *testing.T, timeout time.Duration, dir string, args ...string) (string, string, int, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, anchorBin, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), -1, true
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run %v: %v", args, err)
		}
	}
	return stdout.String(), stderr.String(), code, false
}

func TestCLIAdapterInputRejectsSpecialAndOversizeFiles(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	clean := writeFixture(t, filepath.Join(root, "clean.json"), `{"hook_event_name":"PostToolUse","session_id":"input-session","tool_name":"Bash"}`)
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(clean, link); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"adapter", "capture", "--adapter", "claude-code", "--event", link, "--print"},
		{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", link},
	} {
		if _, stderr, code := runCLI(t, root, args...); code == 0 || !strings.Contains(stderr, "symlink") {
			t.Fatalf("symlink input accepted: %v %s", args, stderr)
		}
	}
	if _, err := os.Stat(clean); err != nil {
		t.Fatal(err)
	}
	dirTarget := t.TempDir()
	for _, args := range [][]string{
		{"adapter", "capture", "--adapter", "claude-code", "--event", dirTarget, "--print"},
		{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", dirTarget},
	} {
		if _, stderr, code := runCLI(t, root, args...); code == 0 || !strings.Contains(stderr, "regular file") {
			t.Fatalf("directory input accepted: %v %s", args, stderr)
		}
	}
	if dev, err := os.Stat("/dev/null"); err == nil && !dev.Mode().IsRegular() {
		for _, args := range [][]string{
			{"adapter", "capture", "--adapter", "claude-code", "--event", "/dev/null", "--print"},
			{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", "/dev/null"},
		} {
			if _, stderr, code := runCLI(t, root, args...); code == 0 || !strings.Contains(stderr, "regular file") {
				t.Fatalf("device input accepted: %v %s", args, stderr)
			}
		}
	}
	if _, err := exec.LookPath("mkfifo"); err == nil {
		fifo := filepath.Join(root, "input.fifo")
		if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
			t.Fatalf("mkfifo: %v %s", err, out)
		}
		for _, args := range [][]string{
			{"adapter", "capture", "--adapter", "claude-code", "--event", fifo, "--print"},
			{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", fifo},
		} {
			out, stderr, code, timedOut := runCLITimeout(t, 2*time.Second, root, args...)
			if timedOut {
				t.Fatalf("FIFO input hung: %v", args)
			}
			if code == 0 || !strings.Contains(stderr, "regular file") {
				t.Fatalf("FIFO input accepted/hung: %v code=%d out=%s err=%s", args, code, out, stderr)
			}
		}
	}
	large := filepath.Join(root, "large.json")
	marker := "XXOVERSIZEXX"
	if err := os.WriteFile(large, bytes.Repeat([]byte(marker), (maxCaptureInput/len(marker))+1), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(eventFiles(t, ws))
	for _, args := range [][]string{
		{"adapter", "capture", "--adapter", "claude-code", "--event", large, "--print"},
		{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", large},
	} {
		if _, stderr, code := runCLI(t, root, args...); code == 0 || !strings.Contains(stderr, "exceeds") {
			t.Fatalf("oversize input accepted: %v %s", args, stderr)
		} else if strings.Contains(stderr, marker) {
			t.Fatalf("oversize error echoed content: %s", stderr)
		}
	}
	if len(eventFiles(t, ws)) != before {
		t.Fatal("rejected oversize ingest wrote an event")
	}
	stdinLarge := strings.Repeat("Y", maxCaptureInput+1)
	for _, args := range [][]string{
		{"adapter", "capture", "--adapter", "claude-code", "--stdin", "--print"},
		{"adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--stdin"},
	} {
		if _, stderr, code := runCLIInput(t, root, stdinLarge, args...); code == 0 || !strings.Contains(stderr, "exceeds") {
			t.Fatalf("oversize stdin accepted: %v %s", args, stderr)
		}
	}
}

func TestCLIAdapterIngestWorkspaceIsolationAndUnverifiedSource(t *testing.T) {
	root := t.TempDir()
	ws1 := filepath.Join(root, "ws1")
	ws2 := filepath.Join(root, "ws2")
	runCLI(t, root, "init", "--dir", ws1, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "init", "--dir", ws2, "--workspace", "ws2", "--device", "dev2")
	runCLI(t, root, "node", "create", "--dir", ws1, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws1, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T1")
	runCLI(t, root, "node", "create", "--dir", ws2, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws2, "--id", "task2", "--type", "task", "--parent", "goal1", "--title", "T2")
	fixture := writeFixture(t, filepath.Join(root, "shared-hook.json"), `{"hook_event_name":"PostToolUse","session_id":"shared-session","cwd":"/tmp/shared","tool_name":"Bash","timestamp":"2026-11-15T00:00:00Z","source_event_id":"source-shared-1","source_seq":1}`)
	out1, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "codex", "--dir", ws1, "--node", "task1", "--event", fixture, "--source", "fixture", "--json")
	if code != 0 {
		t.Fatalf("ws1 ingest failed: %s%s", out1, stderr)
	}
	out2, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "codex", "--dir", ws2, "--node", "task2", "--event", fixture, "--source", "hook_unverified", "--json")
	if code != 0 {
		t.Fatalf("ws2 ingest failed: %s%s", out2, stderr)
	}
	var r1, r2 map[string]any
	_ = json.Unmarshal([]byte(out1), &r1)
	_ = json.Unmarshal([]byte(out2), &r2)
	if r1["session_id"] == "" || r1["session_id"] != r2["session_id"] {
		t.Fatalf("session identity is not stable across workspaces: %v vs %v", r1["session_id"], r2["session_id"])
	}
	if r2["trust_level"] != "unverified_adapter" || r2["source"] != "hook_unverified" {
		t.Fatalf("hook_unverified source was treated as verified: %v", r2)
	}
	status1, _, _ := runCLI(t, root, "status", "--dir", ws1, "--json")
	status2, _, _ := runCLI(t, root, "status", "--dir", ws2, "--json")
	if !strings.Contains(status1, `"node_id": "task1"`) || strings.Contains(status1, `"node_id": "task2"`) {
		t.Fatalf("workspace 1 cross-contaminated: %s", status1)
	}
	if !strings.Contains(status2, `"node_id": "task2"`) || strings.Contains(status2, `"node_id": "task1"`) {
		t.Fatalf("workspace 2 cross-contaminated: %s", status2)
	}
	if strings.Count(status1, `"adapter": "codex"`) != 1 || strings.Count(status2, `"adapter": "codex"`) != 1 {
		t.Fatalf("session registry not workspace-local: ws1=%s ws2=%s", status1, status2)
	}
}

func TestCLIAdapterIngestSourceOrderAndClosedSessionPolicy(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	seen2 := writeFixture(t, filepath.Join(root, "seen2.json"), `{"hook_event_name":"PostToolUse","session_id":"ordered-session","cwd":"/tmp/ordered","tool_name":"Bash","timestamp":"2026-11-15T00:00:02Z","source_event_id":"ordered-2","source_seq":2}`)
	seen1 := writeFixture(t, filepath.Join(root, "seen1.json"), `{"hook_event_name":"PreToolUse","session_id":"ordered-session","cwd":"/tmp/ordered","tool_name":"Read","timestamp":"2026-11-15T00:00:01Z","source_event_id":"ordered-1","source_seq":1}`)
	end := writeFixture(t, filepath.Join(root, "end.json"), `{"hook_event_name":"SessionEnd","session_id":"ordered-session","cwd":"/tmp/ordered","timestamp":"2026-11-15T00:00:03Z","source_event_id":"ordered-end","source_seq":3}`)
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", seen2, "--source", "fixture"); code != 0 {
		t.Fatalf("seen2 failed: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", seen1, "--source", "fixture"); code != 0 {
		t.Fatalf("out-of-order seen1 failed: %s", stderr)
	}
	beforeEnd := len(eventFiles(t, ws))
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", end, "--source", "fixture"); code != 0 {
		t.Fatalf("session_end failed: %s", stderr)
	}
	statusAfterEnd, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(statusAfterEnd, `"ended": true`) || !strings.Contains(statusAfterEnd, `"last_source_seq": 3`) || !strings.Contains(statusAfterEnd, `"historical_observations": 1`) || !strings.Contains(statusAfterEnd, `"status": "todo"`) {
		t.Fatalf("closed-session projection wrong: %s", statusAfterEnd)
	}
	history := writeFixture(t, filepath.Join(root, "history.json"), `{"hook_event_name":"PreToolUse","session_id":"ordered-session","cwd":"/tmp/ordered","tool_name":"Glob","timestamp":"2026-11-15T00:00:00Z","source_event_id":"ordered-history","source_seq":1}`)
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", history, "--source", "fixture"); code != 0 {
		t.Fatalf("historical lower-seq observation after end should be retained as history: %s", stderr)
	}
	statusHistory, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(statusHistory, `"ended": true`) || !strings.Contains(statusHistory, `"last_source_seq": 3`) || !strings.Contains(statusHistory, `"historical_observations": 2`) || !strings.Contains(statusHistory, `"status": "todo"`) {
		t.Fatalf("historical observation changed closed-session projection: %s", statusHistory)
	}
	late := writeFixture(t, filepath.Join(root, "late.json"), `{"hook_event_name":"PostToolUse","session_id":"ordered-session","cwd":"/tmp/ordered","tool_name":"Grep","timestamp":"2026-11-15T00:00:04Z","source_event_id":"ordered-late","source_seq":4}`)
	beforeLate := len(eventFiles(t, ws))
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", late, "--source", "fixture"); code == 0 {
		t.Fatalf("late observation reopened/updated closed session: %s", stderr)
	} else if !strings.Contains(stderr, "late observation after session_end") {
		t.Fatalf("late observation error not explicit: %s", stderr)
	}
	if len(eventFiles(t, ws)) != beforeLate || len(eventFiles(t, ws)) != beforeEnd+2 {
		t.Fatalf("late observation wrote an event: before_end=%d after_end=%d", beforeEnd, len(eventFiles(t, ws)))
	}
	// Re-ingesting the same pre-end source event is idempotent and must not
	// reopen or advance the closed session.
	out, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", seen2, "--source", "fixture", "--json")
	if code != 0 || !strings.Contains(out, `"idempotent_reuse": true`) {
		t.Fatalf("duplicate pre-end source not idempotent: %s%s", out, stderr)
	}
	if len(eventFiles(t, ws)) != beforeLate {
		t.Fatal("duplicate pre-end source wrote an event")
	}
	statusAgain, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(statusAgain, `"ended": true`) || !strings.Contains(statusAgain, `"last_source_seq": 3`) || !strings.Contains(statusAgain, `"status": "todo"`) {
		t.Fatalf("duplicate ingest changed closed-session projection: %s", statusAgain)
	}
	// Replaying the same event set in reverse slice order must produce the same
	// session projection.
	store, err := core.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.LoadReport()
	if err != nil {
		t.Fatal(err)
	}
	events := report.Events
	r1 := core.Replay(events)
	rev := append([]core.Event(nil), events...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	r2 := core.Replay(rev)
	b1, _ := json.Marshal(r1.Tree.Sessions)
	b2, _ := json.Marshal(r2.Tree.Sessions)
	if string(b1) != string(b2) {
		t.Fatalf("session replay depends on slice order:\n%s\n%s", b1, b2)
	}
}

func TestCLIAdapterIngestRequiresStableSourceIdentity(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	base := `{"hook_event_name":"PostToolUse","session_id":"same-meta-session","cwd":"/tmp/same","tool_name":"Bash","prompt":"%s","source_event_id":"%s"}`
	a := writeFixture(t, filepath.Join(root, "a.json"), fmt.Sprintf(base, "prompt-a", "delivery-a"))
	b := writeFixture(t, filepath.Join(root, "b.json"), fmt.Sprintf(base, "prompt-b", "delivery-b"))
	before := len(eventFiles(t, ws))
	for _, f := range []string{a, b} {
		if out, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", f, "--source", "fixture", "--json"); code != 0 {
			t.Fatalf("distinct source id ingest failed: %s%s", out, stderr)
		}
	}
	if len(eventFiles(t, ws)) != before+2 {
		t.Fatalf("distinct source ids were merged: before=%d after=%d", before, len(eventFiles(t, ws)))
	}
	out, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", a, "--source", "fixture", "--json")
	if code != 0 || !strings.Contains(out, `"idempotent_reuse": true`) {
		t.Fatalf("same source id not idempotent: %s%s", out, stderr)
	}
	if len(eventFiles(t, ws)) != before+2 {
		t.Fatal("same source id wrote a second event")
	}
	missing := writeFixture(t, filepath.Join(root, "missing.json"), `{"hook_event_name":"PostToolUse","session_id":"same-meta-session","cwd":"/tmp/same","tool_name":"Bash","prompt":"prompt-c"}`)
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", missing, "--source", "fixture"); code == 0 {
		t.Fatal("missing stable source identity accepted")
	} else if !strings.Contains(stderr, "stable source identity") {
		t.Fatalf("missing identity error not explicit: %s", stderr)
	}
	if len(eventFiles(t, ws)) != before+2 {
		t.Fatal("missing identity wrote an event")
	}
	status, _, _ := runCLI(t, root, "status", "--dir", ws, "--json")
	if !strings.Contains(status, `"adapter": "claude-code"`) || strings.Count(status, `"adapter": "claude-code"`) != 1 {
		t.Fatalf("stable source id created multiple sessions: %s", status)
	}
	assertNoRawCaptureData(t, ws, "delivery-a", "delivery-b", "prompt-a", "prompt-b", "prompt-c", "same-meta-session")
}

func cliSnapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			out[rel] = "symlink:" + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			out[rel] = "file:" + hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCLIRejectsSharedAgentPathsWithoutSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{"real":"config"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(home, ".config", "claude", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "settings.json"), []byte(`{"nested":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(home, ".codex")
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(claude, alias); err != nil {
		t.Fatal(err)
	}
	linkHome := filepath.Join(home, "link-home")
	if err := os.Symlink(home, linkHome); err != nil {
		t.Fatal(err)
	}
	fixture := writeFixture(t, filepath.Join(t.TempDir(), "hook.json"), `{"hook_event_name":"PostToolUse","session_id":"shared-path-session","source_event_id":"shared-path-source"}`)
	before := cliSnapshotTree(t, home)
	for _, dir := range []string{claude, codex, alias, filepath.Join(claude, "nested"), filepath.Join(linkHome, ".claude"), nested} {
		if _, _, code := runCLI(t, t.TempDir(), "adapter", "ingest", "--adapter", "claude-code", "--dir", dir, "--node", "task1", "--event", fixture); code == 0 {
			t.Fatalf("ingest accepted shared/path-alias %s", dir)
		}
		if _, _, code := runCLI(t, t.TempDir(), "init", "--dir", dir, "--workspace", "ws1", "--device", "dev1"); code == 0 {
			t.Fatalf("init accepted shared/path-alias %s", dir)
		}
		if _, _, code := runCLI(t, t.TempDir(), "status", "--dir", dir, "--json"); code == 0 {
			t.Fatalf("status accepted shared/path-alias %s", dir)
		}
	}
	after := cliSnapshotTree(t, home)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("shared path commands changed fake HOME\nbefore=%v\nafter=%v", before, after)
	}
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatal("nonexistent ~/.codex was created")
	}
	for _, dir := range []string{claude, alias} {
		if _, err := os.Stat(filepath.Join(dir, "events")); !os.IsNotExist(err) {
			t.Fatalf("events directory created under %s", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, ".anchor.lock")); !os.IsNotExist(err) {
			t.Fatalf("lock created under %s", dir)
		}
	}
	// Ordinary nonexistent workspaces are not created by Open/status/ingest.
	missing := filepath.Join(home, "missing-workspace")
	if _, _, code := runCLI(t, home, "status", "--dir", missing, "--json"); code == 0 {
		t.Fatal("status accepted a missing workspace")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("failed Open/status created a missing workspace")
	}
}

func eventHashesCLI(t *testing.T, ws string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(ws, "events", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		out[filepath.Base(p)] = hex.EncodeToString(sum[:])
	}
	return out
}

func TestCLIInitRefusesManifestLossAndPreservesLog(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if out, errText, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "old-ws", "--device", "old-dev"); code != 0 {
		t.Fatalf("init failed: %s%s", out, errText)
	}
	if out, errText, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G"); code != 0 {
		t.Fatalf("create failed: %s%s", out, errText)
	}
	before := eventHashesCLI(t, ws)
	if len(before) == 0 {
		t.Fatal("no event batches written")
	}
	saved := filepath.Join(root, "manifest.saved.json")
	if err := os.Rename(filepath.Join(ws, "manifest.json"), saved); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "new-ws", "--device", "new-dev"); code == 0 {
		t.Fatalf("init rebound identity after manifest loss: %s", stderr)
	} else if !strings.Contains(stderr, "existing Anchor data") && !strings.Contains(stderr, "non-empty") {
		t.Fatalf("init refusal was not explicit: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(ws, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("rejected init recreated manifest.json")
	}
	if _, err := os.Stat(filepath.Join(ws, ".anchor.init.lock")); !os.IsNotExist(err) {
		t.Fatal("rejected init left init lock behind")
	}
	after := eventHashesCLI(t, ws)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("event log changed during rejected init\nbefore=%v\nafter=%v", before, after)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T"); code == 0 {
		t.Fatalf("node create succeeded without manifest: %s", stderr)
	}
	if !reflect.DeepEqual(before, eventHashesCLI(t, ws)) {
		t.Fatal("node create without manifest wrote an event")
	}
	// Restoring the original manifest is the explicit recovery path.
	if err := os.Rename(saved, filepath.Join(ws, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 || !strings.Contains(out, `"projection_status": "complete"`) || !strings.Contains(out, `"goal1"`) {
		t.Fatalf("original workspace did not recover after manifest restore: %s", out)
	}
}

func TestCLIConcurrentInitOneIdentity(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	procs := raceCLI(t, root,
		[]string{"init", "--dir", target, "--workspace", "ws-a", "--device", "dev-a"},
		[]string{"init", "--dir", target, "--workspace", "ws-b", "--device", "dev-b"},
	)
	success, failed := 0, 0
	for _, p := range procs {
		if p.code == 0 {
			success++
		} else {
			failed++
			if p.stderr.Len() == 0 {
				t.Fatal("losing init had no diagnostic")
			}
		}
	}
	if success != 1 || failed != 1 {
		t.Fatalf("expected one init winner, got success=%d failed=%d", success, failed)
	}
	b, err := os.ReadFile(filepath.Join(target, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		WorkspaceID     string `json:"workspace_id"`
		DeviceID        string `json:"device_id"`
		EventLogVersion int    `json:"event_log_version"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if !((m.WorkspaceID == "ws-a" && m.DeviceID == "dev-a") || (m.WorkspaceID == "ws-b" && m.DeviceID == "dev-b")) || m.EventLogVersion != 2 {
		t.Fatalf("partial/mixed workspace identity: %+v", m)
	}
	if entries, err := os.ReadDir(filepath.Join(target, "events")); err != nil || len(entries) != 0 {
		t.Fatalf("events directory not empty after init: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(target, ".anchor.init.lock")); !os.IsNotExist(err) {
		t.Fatal("init lock left behind")
	}
	out, _, code := runCLI(t, root, "status", "--dir", target, "--json")
	if code != 0 || !strings.Contains(out, `"projection_status": "complete"`) {
		t.Fatalf("initialized workspace is not openable: %s", out)
	}
}

func TestCLIHandoffExportImportDryRunAndBytes(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	evFile := writeFixture(t, filepath.Join(root, "evidence.txt"), "exported evidence fixture\n")
	runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", evFile)
	sessionEvent := writeFixture(t, filepath.Join(root, "session.json"), `{"hook_event_name":"PostToolUse","session_id":"export-session","source_event_id":"export-source","source_seq":1}`)
	if out, errText, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", sessionEvent, "--source", "fixture"); code != 0 {
		t.Fatalf("session ingest failed: %s%s", out, errText)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("dirty tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("untracked notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "must-not-execute")
	script := fmt.Sprintf("#!/bin/sh\ntouch %s\n", marker)
	if err := os.WriteFile(filepath.Join(repo, "run.sh"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "export-pkg")
	out, stderr, code := runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", pkg, "--include-bytes", "--json")
	if code != 0 {
		t.Fatalf("export failed: %s%s", out, stderr)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatal(err)
	}
	if summary["workspace_id"] != "ws1" || summary["device_id"] != "dev1" || summary["recovery_status"] != "incomplete" || summary["artifact_availability"] != "partial" || summary["code_commit_included"] != false || summary["code_apply"] != "disabled" || summary["adapter_native_recovery"] != "unverified" {
		t.Fatalf("export summary wrong: %s", out)
	}
	mb, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		WorkspaceID string `json:"workspace_id"`
		DeviceID    string `json:"device_id"`
		Packet      struct {
			Tree struct {
				Nodes    []map[string]any `json:"nodes"`
				Sessions []map[string]any `json:"sessions"`
				Evidence []map[string]any `json:"evidence"`
			} `json:"tree"`
			CodeState struct {
				Head  string `json:"head"`
				Dirty bool   `json:"dirty"`
			} `json:"code_state"`
		} `json:"packet"`
		Files []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"files"`
	}
	if err := json.Unmarshal(mb, &manifest); err != nil {
		t.Fatal(err)
	}
	taskFound := false
	for _, n := range manifest.Packet.Tree.Nodes {
		if n["id"] == "task1" {
			taskFound = true
		}
	}
	if manifest.WorkspaceID != "ws1" || manifest.DeviceID != "dev1" || !taskFound || len(manifest.Packet.Tree.Sessions) != 1 || len(manifest.Packet.Tree.Evidence) != 1 || manifest.Packet.CodeState.Head == "" || !manifest.Packet.CodeState.Dirty {
		t.Fatalf("manifest missing recovery data: %s", mb)
	}
	kinds := map[string]int{}
	for _, f := range manifest.Files {
		kinds[f.Kind]++
	}
	if kinds["evidence"] == 0 || kinds["untracked"] < 2 || kinds["git_diff"] == 0 {
		t.Fatalf("selected bytes missing: %+v", kinds)
	}
	target := filepath.Join(root, "import-target")
	dryOut, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target, "--json")
	if code != 0 {
		t.Fatalf("import dry-run failed: %s%s", dryOut, stderr)
	}
	var dry map[string]any
	if err := json.Unmarshal([]byte(dryOut), &dry); err != nil {
		t.Fatal(err)
	}
	if dry["dry_run"] != true || dry["writes"] != float64(0) || dry["recovery_status"] != "incomplete" || dry["code_apply"] != "disabled" || dry["adapter_native_recovery"] != "unverified" || dry["does_not_execute"] != true {
		t.Fatalf("dry-run report wrong: %s", dryOut)
	}
	if dry["workspace_rebuilt"] != true || dry["event_batches"].(float64) < 1 {
		t.Fatalf("dry-run does not plan a workspace rebuild: %s", dryOut)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry-run created target")
	}
	if out, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target, "--apply", "--json"); code != 0 {
		t.Fatalf("actual import failed: %s%s", out, stderr)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Fatalf("import did not rebuild the workspace manifest: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(target, "events")); err != nil || len(entries) == 0 {
		t.Fatalf("import did not restore the event log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "local", "imported", "untracked", "notes.txt")); err != nil {
		t.Fatalf("untracked bytes not staged under local/imported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "anchor-import.json")); err != nil {
		t.Fatal("import report missing")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("import executed package content")
	}
	if _, _, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target, "--apply"); code == 0 {
		t.Fatal("second import overwrote non-empty target")
	}
	// Tampered package bytes are rejected before a new target is created.
	tamperPath := filepath.Join(pkg, "payload", "untracked", "notes.txt")
	if err := os.WriteFile(tamperPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target2 := filepath.Join(root, "import-target-tampered")
	if _, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target2, "--json"); code == 0 {
		t.Fatalf("tampered package accepted: %s", stderr)
	}
	if _, err := os.Stat(target2); !os.IsNotExist(err) {
		t.Fatal("tampered import created target")
	}
	// Metadata-only export is explicit and cannot be called complete.
	metaPkg := filepath.Join(root, "metadata-pkg")
	out, stderr, code = runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", metaPkg, "--json")
	if code != 0 {
		t.Fatalf("metadata export failed: %s%s", out, stderr)
	}
	if !strings.Contains(out, `"artifact_availability": "metadata_only"`) || !strings.Contains(out, `"recovery_status": "incomplete"`) {
		t.Fatalf("metadata-only export overclaimed: %s", out)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		t.Fatal(err)
	}
	// Metadata-only still transports the canonical workspace state so the
	// workspace is restorable; it only omits evidence/artifact bytes.
	if meta["event_batches"].(float64) < 1 || meta["workspace_state_included"] != true {
		t.Fatalf("metadata-only export dropped workspace state: %s", out)
	}
	metaTarget := filepath.Join(root, "metadata-target")
	if _, _, code := runCLI(t, root, "handoff", "import", "--package", metaPkg, "--target", metaTarget, "--json"); code != 0 {
		t.Fatal("metadata package dry-run failed")
	}
	// Secret-bearing untracked bytes abort export and leave no package.
	if err := os.WriteFile(filepath.Join(repo, "secret.txt"), []byte("AKIAIOSFODNN7EXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretPkg := filepath.Join(root, "secret-pkg")
	if _, stderr, code := runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", secretPkg, "--include-bytes", "--json"); code == 0 {
		t.Fatalf("secret-bearing export accepted: %s", stderr)
	}
	if _, err := os.Stat(secretPkg); !os.IsNotExist(err) {
		t.Fatal("secret export left a package")
	}
}

// TestCLIInitAttachInExistingProject covers REQ-WS-001: Anchor must be adoptable
// in an existing project without touching user files.
func TestCLIInitAttachInExistingProject(t *testing.T) {
	project := initRepo(t)
	tracked := filepath.Join(project, "tracked.txt")
	before, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	appFile := filepath.Join(project, "app.go")
	if err := os.WriteFile(appFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, stderr, code := runCLI(t, project, "init", "--attach", "--dir", project, "--workspace", "ws1", "--device", "dev1")
	if code != 0 {
		t.Fatalf("attach failed code=%d stderr=%s stdout=%s", code, stderr, out)
	}
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(canonical, ".anchor")
	if _, err := os.Stat(filepath.Join(state, "manifest.json")); err != nil {
		t.Fatalf("attached manifest missing: %v", err)
	}
	if info, err := os.Stat(filepath.Join(state, "events")); err != nil || !info.IsDir() {
		t.Fatalf("attached events directory missing: %v", err)
	}

	// User files must be untouched.
	after, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("attach modified a project file: %q -> %q", before, after)
	}
	if b, err := os.ReadFile(appFile); err != nil || string(b) != "package main\n" {
		t.Fatalf("attach modified app.go: %q %v", b, err)
	}

	// Attached state must be kept out of the project repository.
	ignore, err := os.ReadFile(filepath.Join(project, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore update: %v", err)
	}
	if !strings.Contains(string(ignore), ".anchor/") {
		t.Fatalf(".gitignore does not ignore attached state: %q", ignore)
	}
	status := exec.Command("git", "-C", project, "status", "--porcelain")
	statusOut, err := status.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(statusOut), ".anchor") {
		t.Fatalf("attached state is visible to the project repo: %s", statusOut)
	}

	// Every command must work with the project directory rather than the state dir.
	out, _, code = runCLI(t, project, "status", "--dir", project, "--repo", project, "--json")
	if code != 0 {
		t.Fatalf("status failed: %s", out)
	}
	var st struct {
		WorkspaceDir string `json:"workspace_dir"`
		Projection   string `json:"projection_status"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st.WorkspaceDir != state || st.Projection != "complete" {
		t.Fatalf("status did not resolve attached state: %+v", st)
	}
	if out, stderr, code = runCLI(t, project, "node", "create", "--dir", project, "--id", "goal1", "--type", "goal", "--title", "Ship"); code != 0 {
		t.Fatalf("node create via project dir failed: %s %s", out, stderr)
	}
	if entries, err := os.ReadDir(filepath.Join(state, "events")); err != nil || len(entries) == 0 {
		t.Fatalf("events were not written into attached state: %v %d", err, len(entries))
	}
	if out, stderr, code = runCLI(t, project, "doctor", "--dir", project, "--repo", project, "--json"); code != 0 {
		t.Fatalf("doctor failed: %s %s", out, stderr)
	}

	// Re-attaching must fail closed instead of re-initializing identity.
	if _, stderr, code = runCLI(t, project, "init", "--attach", "--dir", project, "--workspace", "ws2", "--device", "dev2"); code == 0 {
		t.Fatalf("second attach was accepted: %s", stderr)
	} else if !strings.Contains(stderr, "already has Anchor state") {
		t.Fatalf("unexpected re-attach diagnostic: %s", stderr)
	}

	// Plain init still refuses non-empty directories.
	if _, stderr, code = runCLI(t, project, "init", "--dir", project, "--workspace", "ws3", "--device", "dev3"); code == 0 {
		t.Fatalf("plain init over a project directory was accepted")
	}
}

func TestCLIInitAttachWithoutGitRepoDoesNotTouchGitignore(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, project, "init", "--attach", "--dir", project, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("attach without git repo failed: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(project, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("attach created .gitignore in a non-git directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".anchor", "manifest.json")); err != nil {
		t.Fatalf("attached state missing: %v", err)
	}
}

func TestCLIInitAttachRequiresExistingProject(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent")
	_, stderr, code := runCLI(t, root, "init", "--attach", "--dir", missing, "--workspace", "ws1", "--device", "dev1")
	if code == 0 {
		t.Fatalf("attach to a missing project was accepted")
	}
	if !strings.Contains(stderr, "no such file") && !strings.Contains(stderr, "not exist") {
		t.Fatalf("unexpected diagnostic: %s", stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("attach created the missing project directory: %v", err)
	}
}

// TestCLINodeDetailFieldsAndPacket covers the CLI surface for REQ-ST-001/002/005
// and REQ-RST-001: goal constraints, decision trade-offs and provenance must be
// recordable and must reach the handoff packet.
func TestCLINodeDetailFieldsAndPacket(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "Ship",
		"--north-star", "successor resumes with zero context loss",
		"--must", "keep evidence immutable", "--must-not", "no local causal database",
		"--success", "restore works on a second device"); code != 0 {
		t.Fatalf("goal create: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "dec1", "--type", "decision", "--parent", "goal1",
		"--title", "Hash the packet", "--decision", "hash the body", "--rationale", "detect tampering",
		"--alternative", "sign with a key", "--consequence", "no key management",
		"--provenance", "imported"); code != 0 {
		t.Fatalf("decision create: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "Carry events"); code != 0 {
		t.Fatalf("task create: %s", stderr)
	}
	// Editing must be able to replace the constraint list (a repeated flag states
	// the full list, which keeps edits idempotent instead of accumulating).
	if _, stderr, code := runCLI(t, root, "node", "edit", "--dir", ws, "--id", "goal1",
		"--must", "keep evidence immutable", "--must", "fail closed on unsafe paths"); code != 0 {
		t.Fatalf("goal edit: %s", stderr)
	}
	// Agents may not self-report user confirmation.
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal2", "--type", "goal", "--title", "Bad",
		"--provenance", "user_confirmed"); code == 0 {
		t.Fatalf("user_confirmed provenance was accepted: %s", stderr)
	} else if !strings.Contains(stderr, "local user attestation") {
		t.Fatalf("unexpected diagnostic: %s", stderr)
	}
	// A flag combination that changes nothing must be rejected.
	if _, stderr, code := runCLI(t, root, "node", "edit", "--dir", ws, "--id", "goal1"); code == 0 {
		t.Fatalf("empty edit was accepted: %s", stderr)
	}

	out, stderr, code := runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json")
	if code != 0 {
		t.Fatalf("status: %s %s", out, stderr)
	}
	var st struct {
		Tree struct {
			Nodes map[string]struct {
				NorthStar       string   `json:"north_star"`
				Must            []string `json:"must"`
				MustNot         []string `json:"must_not"`
				SuccessCriteria []string `json:"success_criteria"`
				Decision        string   `json:"decision"`
				Rationale       string   `json:"rationale"`
				Alternatives    []string `json:"alternatives"`
				Consequences    []string `json:"consequences"`
				Provenance      string   `json:"provenance"`
			} `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	goal := st.Tree.Nodes["goal1"]
	if goal.NorthStar != "successor resumes with zero context loss" {
		t.Fatalf("north star not recorded: %+v", goal)
	}
	if len(goal.Must) != 2 || len(goal.MustNot) != 1 || len(goal.SuccessCriteria) != 1 {
		t.Fatalf("goal constraints not recorded: %+v", goal)
	}
	dec := st.Tree.Nodes["dec1"]
	if dec.Decision != "hash the body" || dec.Rationale != "detect tampering" ||
		len(dec.Alternatives) != 1 || len(dec.Consequences) != 1 || dec.Provenance != "imported" {
		t.Fatalf("decision fields not recorded: %+v", dec)
	}

	// The packet must carry the same structure.
	if _, stderr, code := runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo); code != 0 {
		t.Fatalf("handoff prepare: %s", stderr)
	}
	packets, err := filepath.Glob(filepath.Join(ws, "handoff", "*.json"))
	if err != nil || len(packets) != 1 {
		t.Fatalf("packet files: %v %v", packets, err)
	}
	b, err := os.ReadFile(packets[0])
	if err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Goal struct {
			NorthStar       string   `json:"north_star"`
			Must            []string `json:"must"`
			SuccessCriteria []string `json:"success_criteria"`
		} `json:"goal"`
		Decisions []struct {
			Rationale string `json:"rationale"`
		} `json:"decisions"`
		NextSteps []struct {
			ID string `json:"id"`
		} `json:"next_steps"`
		ProvenanceSummary map[string]int `json:"provenance_summary"`
	}
	if err := json.Unmarshal(b, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Goal.NorthStar != "successor resumes with zero context loss" || len(packet.Goal.Must) != 2 {
		t.Fatalf("packet goal block incomplete: %+v", packet.Goal)
	}
	if len(packet.Decisions) != 1 || packet.Decisions[0].Rationale != "detect tampering" {
		t.Fatalf("packet decisions incomplete: %+v", packet.Decisions)
	}
	if len(packet.NextSteps) != 1 || packet.NextSteps[0].ID != "task1" {
		t.Fatalf("packet next steps incomplete: %+v", packet.NextSteps)
	}
	if packet.ProvenanceSummary["imported"] != 1 {
		t.Fatalf("packet provenance summary incomplete: %+v", packet.ProvenanceSummary)
	}
}

// TestCLIExportImportRebuildsWorkspace covers REQ-WS-007 and REQ-RST-006: an
// export must carry the canonical workspace so an import can rebuild a working
// workspace on a fresh device, with an honest verified status.
func TestCLIExportImportRebuildsWorkspace(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "Ship",
		"--north-star", "successor resumes with zero context loss", "--must", "keep evidence immutable")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "Carry the log")
	evFile := writeFixture(t, filepath.Join(root, "evidence.txt"), "round trip evidence fixture\n")
	if _, stderr, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", evFile); code != 0 {
		t.Fatalf("evidence add: %s", stderr)
	}

	pkg := filepath.Join(root, "pkg")
	out, stderr, code := runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", pkg, "--include-bytes", "--json")
	if code != 0 {
		t.Fatalf("export failed: %s%s", out, stderr)
	}
	var ex map[string]any
	if err := json.Unmarshal([]byte(out), &ex); err != nil {
		t.Fatal(err)
	}
	if ex["recovery_status"] != "complete" {
		t.Fatalf("clean workspace export should be complete: %s", out)
	}
	if ex["event_batches"].(float64) < 1 || ex["workspace_state_included"] != true {
		t.Fatalf("export did not carry workspace state: %s", out)
	}
	if ex["evidence_bytes"].(float64) != 1 {
		t.Fatalf("evidence bytes not exported: %s", out)
	}
	if _, err := os.Stat(filepath.Join(pkg, "payload", "workspace", "manifest.json")); err != nil {
		t.Fatal("package has no workspace manifest")
	}
	if _, err := os.Stat(filepath.Join(pkg, "payload", "events")); err != nil {
		t.Fatal("package has no event log")
	}

	target := filepath.Join(root, "restored")
	dryOut, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target, "--json")
	if code != 0 {
		t.Fatalf("dry run failed: %s%s", dryOut, stderr)
	}
	var dry map[string]any
	if err := json.Unmarshal([]byte(dryOut), &dry); err != nil {
		t.Fatal(err)
	}
	if dry["workspace_rebuilt"] != true || dry["recovery_status"] != "complete" {
		t.Fatalf("dry run mispredicted the rebuild: %s", dryOut)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry run created the target")
	}

	appOut, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target, "--apply", "--json")
	if code != 0 {
		t.Fatalf("import failed: %s%s", appOut, stderr)
	}
	var imp map[string]any
	if err := json.Unmarshal([]byte(appOut), &imp); err != nil {
		t.Fatal(err)
	}
	if imp["recovery_status"] != "complete" || imp["workspace_rebuilt"] != true ||
		imp["projection_status"] != "complete" || imp["events_applied"].(float64) < 4 || imp["evidence_verified"].(float64) != 1 {
		t.Fatalf("import report wrong: %s", appOut)
	}
	if imp["workspace_id"] != "ws1" {
		t.Fatalf("import lost workspace identity: %s", appOut)
	}

	// The rebuilt workspace must be a real, usable workspace.
	restoredStatus, stderr, code := runCLI(t, root, "status", "--dir", target, "--repo", repo, "--json")
	if code != 0 {
		t.Fatalf("restored workspace status failed: %s%s", restoredStatus, stderr)
	}
	var st struct {
		Projection string `json:"projection_status"`
		Tree       struct {
			Nodes map[string]struct {
				NorthStar string `json:"north_star"`
				Status    string `json:"status"`
			} `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(restoredStatus), &st); err != nil {
		t.Fatal(err)
	}
	if st.Projection != "complete" {
		t.Fatalf("restored projection incomplete: %s", restoredStatus)
	}
	if st.Tree.Nodes["goal1"].NorthStar != "successor resumes with zero context loss" {
		t.Fatalf("restored goal lost its north star: %s", restoredStatus)
	}
	if st.Tree.Nodes["task1"].Status != "todo" {
		t.Fatalf("restored task missing: %s", restoredStatus)
	}
	if _, _, code := runCLI(t, root, "node", "transition", "--dir", target, "--id", "task1", "--to", "in_progress"); code != 0 {
		t.Fatal("restored workspace is not writable")
	}
	auditOut, _, code := runCLI(t, root, "evidence", "audit", "--dir", target, "--json")
	if code != 0 {
		t.Fatalf("restored evidence audit failed: %s", auditOut)
	}
	var audit struct {
		Referenced []string `json:"referenced_digests"`
		Blobs      []string `json:"blob_files"`
		Orphans    []string `json:"orphan_blobs"`
	}
	if err := json.Unmarshal([]byte(auditOut), &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Referenced) != 1 || len(audit.Blobs) != 1 || len(audit.Orphans) != 0 {
		t.Fatalf("restored evidence not intact: %s", auditOut)
	}

	// Import refuses a non-empty target and a tampered package.
	if _, _, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", target, "--apply"); code == 0 {
		t.Fatal("import overwrote a non-empty target")
	}
	eventFiles, err := filepath.Glob(filepath.Join(pkg, "payload", "events", "*.json"))
	if err != nil || len(eventFiles) == 0 {
		t.Fatalf("no event batches in package: %v", eventFiles)
	}
	original, err := os.ReadFile(eventFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventFiles[0], append(original, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(root, "tampered-target")
	if _, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", tampered, "--apply"); code == 0 {
		t.Fatalf("tampered event log was imported: %s", stderr)
	}
	if _, err := os.Stat(tampered); !os.IsNotExist(err) {
		t.Fatal("tampered import created a target")
	}
}
