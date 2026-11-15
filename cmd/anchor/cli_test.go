package main

import (
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

func eventFiles(t *testing.T, ws string) []map[string]any {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(ws, "events"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ws, "events", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
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
		if e["type"] == "status_changed" {
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
	seqs := map[float64]bool{}
	statusEvents := 0
	for _, e := range events {
		if e["type"] == "status_changed" {
			statusEvents++
		}
		seq, ok := e["device_seq"].(float64)
		if !ok {
			t.Fatalf("missing device_seq: %+v", e)
		}
		if seqs[seq] {
			t.Fatalf("duplicate device_seq %v after concurrent independent writes", seq)
		}
		seqs[seq] = true
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
