package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	fixture := writeFixture(t, filepath.Join(root, "hook.json"), `{"hook_event_name":"PostToolUse","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","tool_name":"Bash","timestamp":"2026-11-15T00:00:00Z","prompt":"must-not-be-copied","tool_input":{"command":"echo must-not-be-copied"}}`)
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
	event2 := writeFixture(t, filepath.Join(root, "hook2.json"), `{"hook_event_name":"PostToolUse","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","tool_name":"Read","timestamp":"2026-11-15T00:00:02Z"}`)
	event3 := writeFixture(t, filepath.Join(root, "hook3.json"), `{"hook_event_name":"PostToolUse","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","tool_name":"Grep","timestamp":"2026-11-14T00:00:00Z"}`)
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
	endUnknown := writeFixture(t, filepath.Join(root, "end-unknown.json"), `{"hook_event_name":"SessionEnd","session_id":"unknown-session","cwd":"/tmp/raw-cwd/project","timestamp":"2026-11-15T00:01:00Z"}`)
	beforeBad := len(eventFiles(t, ws))
	if _, stderr, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", endUnknown, "--source", "fixture"); code == 0 {
		t.Fatalf("session_end before session_seen accepted: %s", stderr)
	}
	if len(eventFiles(t, ws)) != beforeBad {
		t.Fatal("rejected session_end wrote an event")
	}

	// SessionEnd is a lightweight marker: it must not mark the task done.
	end := writeFixture(t, filepath.Join(root, "end.json"), `{"hook_event_name":"SessionEnd","session_id":"raw-session-id","cwd":"/tmp/raw-cwd/project","timestamp":"2026-11-15T00:03:00Z"}`)
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
	noSession := writeFixture(t, filepath.Join(root, "no-session.json"), `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`)
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", noSession); code == 0 {
		t.Fatal("missing session_id accepted")
	}
	badSource := writeFixture(t, filepath.Join(root, "bad-source.json"), `{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash"}`)
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws, "--node", "task1", "--event", badSource, "--source", "native"); code == 0 {
		t.Fatal("unverified native source accepted")
	}
	secretFile := writeFixture(t, filepath.Join(root, "secret-hook.json"), `{"hook_event_name":"PostToolUse","session_id":"secret-session","tool_name":"Bash","prompt":"AKIAIOSFODNN7EXAMPLE"}`)
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
	fixture := writeFixture(t, filepath.Join(root, "hook-exec.json"), `{"hook_event_name":"PostToolUse","session_id":"session-exec","cwd":"/tmp/fixture","tool_name":"Bash","tool_input":{"command":"touch `+pwn+`"}}`)
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
	fixture := writeFixture(t, filepath.Join(root, "shared-hook.json"), `{"hook_event_name":"PostToolUse","session_id":"shared-session","cwd":"/tmp/shared","tool_name":"Bash","timestamp":"2026-11-15T00:00:00Z"}`)
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
