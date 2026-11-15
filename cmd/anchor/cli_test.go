package main

import (
	"bytes"
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
