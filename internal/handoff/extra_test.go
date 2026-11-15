package handoff

// Extra coverage tests for handoff. They live in a separate file so the
// original tests stay untouched; helpers from handoff_test.go are reused.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/core"
)

func runGitT(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func writeFileT(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func chmodT(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

// appendStatusT appends a status_changed mutation using the ownership lease
// created by appendOwnership. baseRevision is the node revision before the
// change; parents must name the latest node event.
func appendStatusT(t *testing.T, dir, node string, to core.Status, baseRevision int, lease string, parents ...string) string {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(core.NodePayload{NodeID: node, Status: string(to)})
	e, _, err := store.Append(core.Event{
		Type: core.EvStatusChanged, CausalParents: parents,
		BaseRevision: baseRevision, ExpectedRevision: baseRevision + 1,
		LeaseID: lease, FencingToken: 1, Payload: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e.EventID
}

func appendTombstoneT(t *testing.T, dir, node string, baseRevision int, lease string, parents ...string) string {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(core.NodePayload{NodeID: node})
	e, _, err := store.Append(core.Event{
		Type: core.EvNodeTombstoned, CausalParents: parents,
		BaseRevision: baseRevision, ExpectedRevision: baseRevision + 1,
		LeaseID: lease, FencingToken: 1, Payload: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e.EventID
}

// writePendingBatchT writes a hash-valid event batch whose only event names a
// missing causal parent, which makes replay report an incomplete projection.
func writePendingBatchT(t *testing.T, stateDir string) {
	t.Helper()
	ev := core.Event{
		SchemaVersion: core.SchemaVersion,
		EventID:       "pending-evt-1",
		WorkspaceID:   "ws-test",
		DeviceID:      "dev-test",
		DeviceSeq:     1,
		Type:          core.EvNodeCreated,
		CausalParents: []string{"missing-parent"},
		Payload:       json.RawMessage(`{"node_id":"pending1","type":"goal","title":"pending"}`),
	}
	if err := ev.Seal(); err != nil {
		t.Fatal(err)
	}
	events := []core.Event{ev}
	body, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	env := struct {
		AnchorBatch  int          `json:"anchor_batch"`
		BatchID      string       `json:"batch_id"`
		EventCount   int          `json:"event_count"`
		EventsSHA256 string       `json:"events_sha256"`
		Events       []core.Event `json:"events"`
	}{1, "pendingbatch1", len(events), "sha256:" + hex.EncodeToString(sum[:]), events}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "events", "batch_pending.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nodeMap(ns ...*core.Node) map[string]*core.Node {
	m := make(map[string]*core.Node, len(ns))
	for _, n := range ns {
		m[n.ID] = n
	}
	return m
}

func TestCodeStateCleanAndDirtyVariants(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		repo := initRepo(t)
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if cs.Branch == "" || cs.Branch == "detached" {
			t.Fatalf("clean repo must be on a branch, got %q", cs.Branch)
		}
		if cs.Head != strings.TrimSpace(runGitT(t, repo, "rev-parse", "HEAD")) {
			t.Fatalf("head mismatch: %+v", cs)
		}
		if len(cs.Head) != 40 || len(cs.Tree) != 40 {
			t.Fatalf("head/tree must be full object ids: %+v", cs)
		}
		if cs.Dirty || cs.StagedCount != 0 || cs.UnstagedCount != 0 || cs.UntrackedCount != 0 {
			t.Fatalf("clean repo reported dirty: %+v", cs)
		}
		if len(cs.ChangedFiles) != 0 || len(cs.Untracked) != 0 {
			t.Fatalf("clean repo carried files: %+v", cs)
		}
		if cs.StagedDiffSHA256 != "" || cs.UnstagedDiffSHA256 != "" {
			t.Fatalf("clean repo carried diff hashes: %+v", cs)
		}
	})

	t.Run("staged only", func(t *testing.T) {
		repo := initRepo(t)
		writeFileT(t, repo, "tracked.txt", "staged change\n")
		runGitT(t, repo, "add", "tracked.txt")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if !cs.Dirty || cs.StagedCount != 1 || cs.UnstagedCount != 0 {
			t.Fatalf("unexpected counts: %+v", cs)
		}
		if cs.StagedDiffSHA256 == "" {
			t.Fatalf("staged diff hash missing: %+v", cs)
		}
		want := hashBytes([]byte(runGitT(t, repo, "diff", "--cached", "--binary")))
		if cs.StagedDiffSHA256 != want {
			t.Fatalf("staged diff hash = %s, want %s", cs.StagedDiffSHA256, want)
		}
		if cs.UnstagedDiffSHA256 != "" {
			t.Fatalf("staged-only repo has an unstaged diff: %+v", cs)
		}
		if len(cs.ChangedFiles) != 1 || cs.ChangedFiles[0] != "tracked.txt" {
			t.Fatalf("changed files = %v", cs.ChangedFiles)
		}
	})

	t.Run("unstaged only", func(t *testing.T) {
		repo := initRepo(t)
		writeFileT(t, repo, "tracked.txt", "unstaged change\n")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if !cs.Dirty || cs.StagedCount != 0 || cs.UnstagedCount != 1 {
			t.Fatalf("unexpected counts: %+v", cs)
		}
		if cs.StagedDiffSHA256 != "" || cs.UnstagedDiffSHA256 == "" {
			t.Fatalf("unexpected diff hashes: %+v", cs)
		}
		want := hashBytes([]byte(runGitT(t, repo, "diff", "--binary")))
		if cs.UnstagedDiffSHA256 != want {
			t.Fatalf("unstaged diff hash = %s, want %s", cs.UnstagedDiffSHA256, want)
		}
	})

	t.Run("both diffs", func(t *testing.T) {
		repo := initRepo(t)
		writeFileT(t, repo, "tracked.txt", "staged half\n")
		runGitT(t, repo, "add", "tracked.txt")
		writeFileT(t, repo, "tracked.txt", "unstaged half\n")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if cs.StagedCount != 1 || cs.UnstagedCount != 1 {
			t.Fatalf("expected one staged and one unstaged change, got %+v", cs)
		}
		if cs.StagedDiffSHA256 == "" || cs.UnstagedDiffSHA256 == "" || cs.StagedDiffSHA256 == cs.UnstagedDiffSHA256 {
			t.Fatalf("expected distinct staged/unstaged diff hashes: %+v", cs)
		}
	})

	t.Run("rename skips original path token", func(t *testing.T) {
		repo := initRepo(t)
		runGitT(t, repo, "mv", "tracked.txt", "renamed.txt")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if cs.StagedCount != 1 || cs.UnstagedCount != 0 {
			t.Fatalf("rename counts: %+v", cs)
		}
		if len(cs.ChangedFiles) != 1 || cs.ChangedFiles[0] != "renamed.txt" {
			t.Fatalf("rename path parser leaked the original token: %v", cs.ChangedFiles)
		}
	})

	t.Run("unsafe status paths are dropped but still counted", func(t *testing.T) {
		repo := initRepo(t)
		writeFileT(t, repo, "weird:name.txt", "one\n")
		runGitT(t, repo, "add", "weird:name.txt")
		runGitT(t, repo, "commit", "-qm", "colon")
		writeFileT(t, repo, "weird:name.txt", "two\n")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if cs.UnstagedCount != 1 {
			t.Fatalf("colon path not counted: %+v", cs)
		}
		for _, p := range cs.ChangedFiles {
			if strings.Contains(p, ":") {
				t.Fatalf("unsafe path leaked into changed files: %v", cs.ChangedFiles)
			}
		}
	})
}

func TestCodeStateUntrackedVariants(t *testing.T) {
	t.Run("regular file is hashed", func(t *testing.T) {
		repo := initRepo(t)
		writeFileT(t, repo, "untracked.txt", "hello\n")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if !cs.Dirty || cs.UntrackedCount != 1 || len(cs.Untracked) != 1 {
			t.Fatalf("untracked not captured: %+v", cs)
		}
		f := cs.Untracked[0]
		if f.Path != "untracked.txt" || f.Size != 6 || f.Skipped || f.Reason != "" {
			t.Fatalf("unexpected untracked ref: %+v", f)
		}
		if f.SHA256 != hashBytes([]byte("hello\n")) {
			t.Fatalf("untracked hash = %s", f.SHA256)
		}
	})

	t.Run("oversize file is skipped", func(t *testing.T) {
		repo := initRepo(t)
		p := writeFileT(t, repo, "big.bin", "")
		if err := os.Truncate(p, 10<<20+1); err != nil {
			t.Fatal(err)
		}
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Untracked) != 1 {
			t.Fatalf("untracked = %+v", cs.Untracked)
		}
		f := cs.Untracked[0]
		if !f.Skipped || f.Reason != "size limit" || f.SHA256 != "" || f.Size != 10<<20+1 {
			t.Fatalf("oversize untracked not skipped: %+v", f)
		}
	})

	t.Run("unreadable file is skipped", func(t *testing.T) {
		repo := initRepo(t)
		p := writeFileT(t, repo, "unreadable.txt", "clean\n")
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		if _, err := os.ReadFile(p); err == nil {
			t.Skipf("cannot make a file unreadable for uid %d", os.Geteuid())
		}
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Untracked) != 1 || !cs.Untracked[0].Skipped || cs.Untracked[0].Reason != "read error" {
			t.Fatalf("unreadable untracked not skipped: %+v", cs.Untracked)
		}
	})

	t.Run("symlink and unsafe names are skipped", func(t *testing.T) {
		repo := initRepo(t)
		target := filepath.Join(t.TempDir(), "target.txt")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(repo, "link.txt")); err != nil {
			t.Fatal(err)
		}
		writeFileT(t, repo, "weird:name.txt", "x")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs.Untracked) != 1 {
			t.Fatalf("expected only the symlink to be reported, got %+v", cs.Untracked)
		}
		f := cs.Untracked[0]
		if f.Path != "link.txt" || !f.Skipped || f.Reason != "not a regular file" {
			t.Fatalf("symlink untracked not skipped: %+v", f)
		}
	})
}

func TestCodeStateDetachedHeadAndNoCommits(t *testing.T) {
	t.Run("detached head", func(t *testing.T) {
		repo := initRepo(t)
		runGitT(t, repo, "checkout", "-q", "--detach")
		cs, err := codeState(repo)
		if err != nil {
			t.Fatal(err)
		}
		if cs.Branch != "detached" {
			t.Fatalf("detached HEAD branch = %q, want %q", cs.Branch, "detached")
		}
		if len(cs.Head) != 40 || cs.Dirty {
			t.Fatalf("detached state = %+v", cs)
		}
	})

	t.Run("no commits", func(t *testing.T) {
		repo := t.TempDir()
		runGitT(t, repo, "init", "-q")
		_, err := codeState(repo)
		if err == nil {
			t.Fatal("repository without commits was accepted")
		}
		if !strings.Contains(err.Error(), "repository has no HEAD commit") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestGitHelperFailureIncludesCommand(t *testing.T) {
	notRepo := t.TempDir()
	_, err := git(notRepo, "status", "--porcelain=v1")
	if err == nil {
		t.Fatal("git helper accepted a non-repository directory")
	}
	if !strings.Contains(err.Error(), "git status --porcelain=v1:") {
		t.Fatalf("error does not name the failed command: %v", err)
	}
	if _, err := git(initRepo(t), "rev-parse", "HEAD"); err != nil {
		t.Fatalf("git helper failed on a real repo: %v", err)
	}
}

func TestTreeSummaryDirect(t *testing.T) {
	tree := core.NewTree()
	tree.Nodes = nodeMap(
		&core.Node{ID: "g1", Type: core.NodeGoal, Status: core.StatusInProgress},
		&core.Node{ID: "g2", Type: core.NodeGoal, Status: core.StatusInProgress, Deleted: true},
		&core.Node{ID: "a1", Type: core.NodeTask, Status: core.StatusActive},
		&core.Node{ID: "a2", Type: core.NodeBranch, Status: core.StatusActive},
	)
	tree.Evidence = map[string]core.EvidenceEntry{
		"ev2": {EvidenceID: "ev2", NodeID: "a1"},
		"ev1": {EvidenceID: "ev1", NodeID: "a1"},
	}
	tree.Sessions = map[string]core.Session{
		"s2": {ID: "s2"},
		"s1": {ID: "s1"},
	}
	events := []core.Event{{WorkspaceID: "ws-x"}, {WorkspaceID: "ws-y"}}
	res := core.ReplayResult{
		Tree:           tree,
		RecoveryStatus: "incomplete",
		Pending:        []string{"p1"},
		Conflicts:      []core.Conflict{{EventID: "c1", Reason: "cycle"}},
	}
	ts := treeSummary(events, res)
	if ts.WorkspaceID != "ws-x" {
		t.Fatalf("workspace id = %q", ts.WorkspaceID)
	}
	if ts.ProjectionStatus != "incomplete" || len(ts.Pending) != 1 || len(ts.Conflicts) != 1 {
		t.Fatalf("projection metadata lost: %+v", ts)
	}
	if len(ts.GoalIDs) != 1 || ts.GoalIDs[0] != "g1" {
		t.Fatalf("goal ids = %v, tombstoned goal must be excluded", ts.GoalIDs)
	}
	if ts.ActiveNodeID != "a1" {
		t.Fatalf("active node = %q, want a1", ts.ActiveNodeID)
	}
	if len(ts.Sessions) != 2 || ts.Sessions[0].ID != "s1" || ts.Sessions[1].ID != "s2" {
		t.Fatalf("sessions not sorted: %+v", ts.Sessions)
	}
	if len(ts.Evidence) != 2 || ts.Evidence[0].EvidenceID != "ev1" || ts.Evidence[1].EvidenceID != "ev2" {
		t.Fatalf("evidence not sorted: %+v", ts.Evidence)
	}
	if len(ts.Nodes) != 4 || ts.Nodes[0].ID != "a1" {
		t.Fatalf("nodes not sorted by id: %+v", ts.Nodes)
	}
}

func TestTreeSummaryEmptyEventsAndTree(t *testing.T) {
	ts := treeSummary(nil, core.ReplayResult{Tree: core.NewTree(), RecoveryStatus: "complete"})
	if ts.WorkspaceID != "" || len(ts.Nodes) != 0 || len(ts.GoalIDs) != 0 || ts.ActiveNodeID != "" {
		t.Fatalf("empty summary = %+v", ts)
	}
}

func TestDetailSummaryNoGoalAndSingleGoal(t *testing.T) {
	t.Run("no goal", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(&core.Node{ID: "t1", Type: core.NodeTask, Title: "do it"})
		goal, decisions, next, questions, ownership, prov := detailSummary(tree)
		if goal != nil || len(decisions) != 0 || len(questions) != 0 || len(ownership) != 0 {
			t.Fatalf("unexpected summary: goal=%+v decisions=%+v questions=%+v ownership=%+v", goal, decisions, questions, ownership)
		}
		if len(next) != 1 || next[0].ID != "t1" {
			t.Fatalf("next = %+v", next)
		}
		if prov["unspecified"] != 1 {
			t.Fatalf("provenance = %+v", prov)
		}
	})

	t.Run("empty tree yields nil provenance", func(t *testing.T) {
		goal, decisions, next, questions, ownership, prov := detailSummary(core.NewTree())
		if goal != nil || decisions != nil || next != nil || questions != nil || ownership != nil || prov != nil {
			t.Fatalf("empty tree summary = %+v %+v %+v %+v %+v %+v", goal, decisions, next, questions, ownership, prov)
		}
	})

	t.Run("goal without north star", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(&core.Node{ID: "g1", Type: core.NodeGoal, Title: "ship", Status: core.StatusInProgress})
		goal, _, _, _, _, prov := detailSummary(tree)
		if goal == nil {
			t.Fatal("goal missing")
		}
		if goal.ID != "g1" || goal.Title != "ship" || goal.NorthStar != "" || goal.Status != string(core.StatusInProgress) {
			t.Fatalf("goal = %+v", goal)
		}
		if goal.Provenance != "unspecified" || prov["unspecified"] != 1 {
			t.Fatalf("provenance = %q %+v", goal.Provenance, prov)
		}
	})

	t.Run("tombstoned goal is excluded and not counted in provenance", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(&core.Node{ID: "g1", Type: core.NodeGoal, Deleted: true})
		goal, _, _, _, _, prov := detailSummary(tree)
		if goal != nil {
			t.Fatalf("tombstoned goal selected: %+v", goal)
		}
		if prov != nil {
			t.Fatalf("tombstoned nodes must not contribute provenance: %+v", prov)
		}
	})

	t.Run("two pending goals only first id is summarized", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(
			&core.Node{ID: "g2", Type: core.NodeGoal, Title: "second", Status: core.StatusTodo},
			&core.Node{ID: "g1", Type: core.NodeGoal, Title: "first", Status: core.StatusTodo},
		)
		goal, _, _, _, _, _ := detailSummary(tree)
		if goal == nil || goal.ID != "g1" || goal.Title != "first" {
			t.Fatalf("expected the lowest-id pending goal, got %+v", goal)
		}
	})

	t.Run("goal rank prefers active over pending over abandoned", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(
			&core.Node{ID: "g1", Type: core.NodeGoal, Title: "abandoned", Status: core.StatusAbandoned},
			&core.Node{ID: "g2", Type: core.NodeGoal, Title: "pending", Status: core.StatusTodo},
			&core.Node{ID: "g3", Type: core.NodeGoal, Title: "active", Status: core.StatusInProgress},
		)
		goal, _, _, _, _, _ := detailSummary(tree)
		if goal == nil || goal.ID != "g3" || goal.Status != string(core.StatusInProgress) {
			t.Fatalf("active goal must win, got %+v", goal)
		}
		delete(tree.Nodes, "g3")
		goal, _, _, _, _, _ = detailSummary(tree)
		if goal == nil || goal.ID != "g2" {
			t.Fatalf("pending goal must beat abandoned, got %+v", goal)
		}
		delete(tree.Nodes, "g2")
		goal, _, _, _, _, _ = detailSummary(tree)
		if goal == nil || goal.ID != "g1" || goal.Status != string(core.StatusAbandoned) {
			t.Fatalf("abandoned goal must be the fallback, got %+v", goal)
		}
	})
}

func TestDetailSummaryDecisionsAndNextSteps(t *testing.T) {
	t.Run("decisions carry trade-offs and are sorted", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(
			&core.Node{ID: "d2", Type: core.NodeDecision, Title: "later", Decision: "b", Rationale: "r2",
				Alternatives: []string{"alt2"}, Consequences: []string{"cons2"}, Status: core.StatusBlocked,
				Provenance: core.ProvenanceImported},
			&core.Node{ID: "d1", Type: core.NodeDecision, Title: "first", Decision: "a", Rationale: "r1",
				Alternatives: []string{"alt1"}, Consequences: []string{"cons1"}, Status: core.StatusDone,
				Provenance: core.ProvenanceAgentInferred},
		)
		_, decisions, _, _, _, prov := detailSummary(tree)
		if len(decisions) != 2 || decisions[0].ID != "d1" || decisions[1].ID != "d2" {
			t.Fatalf("decisions not sorted: %+v", decisions)
		}
		d := decisions[0]
		if d.Decision != "a" || d.Rationale != "r1" || len(d.Alternatives) != 1 || len(d.Consequences) != 1 ||
			d.Status != string(core.StatusDone) || d.Provenance != string(core.ProvenanceAgentInferred) {
			t.Fatalf("decision lost trade-offs: %+v", d)
		}
		if prov[string(core.ProvenanceImported)] != 1 || prov[string(core.ProvenanceAgentInferred)] != 1 {
			t.Fatalf("provenance = %+v", prov)
		}
	})

	t.Run("next steps rank and exclude finished tasks", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(
			&core.Node{ID: "t1", Type: core.NodeTask, Status: core.StatusTodo},
			&core.Node{ID: "t2", Type: core.NodeTask, Status: core.StatusInProgress},
			&core.Node{ID: "t3", Type: core.NodeTask, Status: core.StatusBlocked},
			&core.Node{ID: "t4", Type: core.NodeTask, Status: core.StatusDone},
			&core.Node{ID: "t5", Type: core.NodeTask, Status: core.StatusAbandoned},
			&core.Node{ID: "t6", Type: core.NodeTask, Status: core.StatusInProgress},
		)
		_, _, next, _, _, _ := detailSummary(tree)
		var ids []string
		for _, n := range next {
			ids = append(ids, n.ID)
		}
		want := []string{"t2", "t6", "t3", "t1"}
		if strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Fatalf("next step ordering = %v, want %v", ids, want)
		}
	})

	t.Run("open questions exclude finished questions", func(t *testing.T) {
		tree := core.NewTree()
		tree.Nodes = nodeMap(
			&core.Node{ID: "q1", Type: core.NodeQuestion, Status: core.StatusTodo},
			&core.Node{ID: "q2", Type: core.NodeQuestion, Status: core.StatusDone},
			&core.Node{ID: "q3", Type: core.NodeQuestion, Status: core.StatusAbandoned},
			&core.Node{ID: "q4", Type: core.NodeQuestion, Status: core.StatusBlocked},
		)
		_, _, _, questions, _, _ := detailSummary(tree)
		if len(questions) != 2 || questions[0].ID != "q1" || questions[1].ID != "q4" {
			t.Fatalf("questions = %+v", questions)
		}
	})
}

func TestDetailSummaryOwnershipAndProvenance(t *testing.T) {
	tree := core.NewTree()
	tree.Nodes = nodeMap(
		&core.Node{ID: "n1", Type: core.NodeGoal, Owner: "agent-a", LeaseID: "l1", FencingToken: 3,
			Provenance: core.ProvenanceUserConfirmed},
		&core.Node{ID: "n2", Type: core.NodeTask, Owner: "agent-b", LeaseID: "l2",
			Provenance: core.ProvenanceImported},
		&core.Node{ID: "n3", Type: core.NodeTask, Owner: "agent-c", Deleted: true},
		&core.Node{ID: "n4", Type: core.NodeTask},
	)
	goal, _, _, _, ownership, prov := detailSummary(tree)
	if goal == nil || goal.ID != "n1" || goal.Provenance != string(core.ProvenanceUserConfirmed) {
		t.Fatalf("goal = %+v", goal)
	}
	if len(ownership) != 2 || ownership[0].NodeID != "n1" || ownership[1].NodeID != "n2" {
		t.Fatalf("ownership = %+v", ownership)
	}
	if ownership[0].Owner != "agent-a" || ownership[0].LeaseID != "l1" || ownership[0].FencingToken != 3 {
		t.Fatalf("ownership ref lost lease data: %+v", ownership[0])
	}
	wantProv := map[string]int{
		string(core.ProvenanceUserConfirmed): 1,
		string(core.ProvenanceImported):      1,
		"unspecified":                        1, // n3 is tombstoned and must not count
	}
	for k, v := range wantProv {
		if prov[k] != v {
			t.Fatalf("provenance[%q] = %d, want %d (%+v)", k, prov[k], v, prov)
		}
	}
}

// structuredWorkspace seeds a workspace with a goal, decision, task, question
// and ownership events, returning it initialized and ready for Prepare.
func structuredWorkspace(t *testing.T) (string, string) {
	t.Helper()
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	gc := appendOwnership(t, dir, "g1", "lg")
	appendNode(t, dir, "g1", "goal", "Ship local segment", "", "lg", core.NodePayload{
		NorthStar:  "successor resumes with zero context loss",
		Must:       []string{"keep evidence immutable"},
		Provenance: string(core.ProvenanceAgentInferred),
	}, gc)
	dc := appendOwnership(t, dir, "d1", "ld", gc)
	appendNode(t, dir, "d1", "decision", "Hash the packet", "g1", "ld", core.NodePayload{
		Decision: "hash the packet body", Rationale: "detect tampering",
		Alternatives: []string{"sign with a key"}, Provenance: string(core.ProvenanceImported),
	}, dc)
	tc := appendOwnership(t, dir, "t1", "lt", gc)
	appendNode(t, dir, "t1", "task", "Carry the event log", "g1", "lt", core.NodePayload{}, tc)
	qc := appendOwnership(t, dir, "q1", "lq", gc)
	appendNode(t, dir, "q1", "question", "Which sync topology?", "g1", "lq", core.NodePayload{}, qc)
	return dir, repo
}

func TestPrepareCarriesTaskStructureAndRoundTrips(t *testing.T) {
	dir, repo := structuredWorkspace(t)
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != 1 || !strings.HasPrefix(p.HandoffID, "ho_") || p.CreatedAt.IsZero() {
		t.Fatalf("packet identity incomplete: %+v", p)
	}
	if p.RecoveryStatus != StatusComplete || len(p.Warnings) != 0 {
		t.Fatalf("clean structured workspace must be complete without warnings: status=%s warnings=%v", p.RecoveryStatus, p.Warnings)
	}
	if p.ArtifactAvailability != "metadata_only" {
		t.Fatalf("availability = %q", p.ArtifactAvailability)
	}
	if len(p.ContentHash) != 64 {
		t.Fatalf("content hash = %q", p.ContentHash)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(realDir, "handoff")
	if filepath.Dir(path) != wantDir || filepath.Base(path) != p.HandoffID+".json" {
		t.Fatalf("packet path = %q, want parent %q", path, wantDir)
	}
	if len(p.Tree.GoalIDs) != 1 || p.Tree.GoalIDs[0] != "g1" || len(p.Tree.Nodes) != 4 {
		t.Fatalf("tree summary = %+v", p.Tree)
	}
	if p.Goal == nil || p.Goal.ID != "g1" || p.Goal.NorthStar == "" || len(p.Goal.Must) != 1 {
		t.Fatalf("goal block = %+v", p.Goal)
	}
	if len(p.Decisions) != 1 || p.Decisions[0].ID != "d1" {
		t.Fatalf("decisions = %+v", p.Decisions)
	}
	if len(p.NextSteps) != 1 || p.NextSteps[0].ID != "t1" {
		t.Fatalf("next steps = %+v", p.NextSteps)
	}
	if len(p.OpenQuestions) != 1 || p.OpenQuestions[0].ID != "q1" {
		t.Fatalf("questions = %+v", p.OpenQuestions)
	}
	if len(p.Ownership) != 4 {
		t.Fatalf("ownership = %+v", p.Ownership)
	}
	if p.ProvenanceSummary[string(core.ProvenanceAgentInferred)] != 1 ||
		p.ProvenanceSummary[string(core.ProvenanceImported)] != 1 ||
		p.ProvenanceSummary["unspecified"] != 2 {
		t.Fatalf("provenance summary = %+v", p.ProvenanceSummary)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentHash != p.ContentHash || loaded.HandoffID != p.HandoffID || loaded.Goal == nil ||
		loaded.Goal.NorthStar != p.Goal.NorthStar || len(loaded.Decisions) != 1 || len(loaded.Tree.Nodes) != 4 {
		t.Fatalf("round-trip lost content: %+v", loaded)
	}
}

func TestPrepareCleanWorkspaceWithoutGoalWarns(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusComplete {
		t.Fatalf("missing goal must only warn, got %s (%v)", p.RecoveryStatus, p.Warnings)
	}
	if len(p.Warnings) != 1 || p.Warnings[0] != "no goal node in the workspace; the packet carries no north star or constraints" {
		t.Fatalf("warnings = %v", p.Warnings)
	}
	if p.Goal != nil {
		t.Fatalf("goal block = %+v", p.Goal)
	}
}

func TestPrepareGoalWithoutNorthStarWarns(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)
	gc := appendOwnership(t, dir, "g1", "lg")
	appendNode(t, dir, "g1", "goal", "no north star", "", "lg", core.NodePayload{}, gc)
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusComplete {
		t.Fatalf("status = %s", p.RecoveryStatus)
	}
	if len(p.Warnings) != 1 || p.Warnings[0] != "goal g1 has no north star recorded" {
		t.Fatalf("warnings = %v", p.Warnings)
	}
}

func TestPrepareDirtyRepoIsIncomplete(t *testing.T) {
	dir, repo := structuredWorkspace(t)
	writeFileT(t, repo, "tracked.txt", "dirty\n")
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusIncomplete {
		t.Fatalf("dirty repo packet status = %s", p.RecoveryStatus)
	}
	if !p.CodeState.Dirty || p.CodeState.UnstagedCount != 1 {
		t.Fatalf("code state = %+v", p.CodeState)
	}
	found := false
	for _, w := range p.Warnings {
		if w == "uncommitted code is metadata-only; artifact bytes were not captured" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing dirty-code warning: %v", p.Warnings)
	}
}

func TestPrepareQuarantinedBatchIsIncomplete(t *testing.T) {
	dir, repo := structuredWorkspace(t)
	writeFileT(t, dir, "events/bad.json", "{not json")
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusIncomplete {
		t.Fatalf("quarantined packet status = %s", p.RecoveryStatus)
	}
	if len(p.Quarantined) != 1 || p.Quarantined[0].File != "bad.json" {
		t.Fatalf("quarantined = %+v", p.Quarantined)
	}
	found := false
	for _, w := range p.Warnings {
		if w == "some event files are quarantined; recovery is incomplete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing quarantine warning: %v", p.Warnings)
	}
}

func TestPrepareProjectionIncomplete(t *testing.T) {
	dir, repo := structuredWorkspace(t)
	writePendingBatchT(t, dir)
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusIncomplete {
		t.Fatalf("projection-incomplete packet status = %s", p.RecoveryStatus)
	}
	found := false
	for _, w := range p.Warnings {
		if w == "projection has pending events or conflicts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing projection warning: %v", p.Warnings)
	}
}

func TestPrepareAttachedLayoutStateDir(t *testing.T) {
	project := t.TempDir()
	store, state, err := core.InitAttached(project, "ws-attached", "dev-attached")
	if err != nil {
		t.Fatal(err)
	}
	realProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if state != filepath.Join(realProject, ".anchor") {
		t.Fatalf("attached state dir = %q, want %q", state, filepath.Join(realProject, ".anchor"))
	}
	repo := initRepo(t)
	gc := appendOwnership(t, project, "g1", "lg")
	appendNode(t, project, "g1", "goal", "attached goal", "", "lg", core.NodePayload{NorthStar: "stay attached"}, gc)
	p, path, err := Prepare(project, repo)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(realProject, ".anchor", "handoff")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("packet path = %q, want parent %q", path, wantDir)
	}
	if len(p.Tree.GoalIDs) != 1 || p.Tree.GoalIDs[0] != "g1" {
		t.Fatalf("attached tree summary = %+v", p.Tree)
	}
	got, gotPath, err := Latest(project)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "" && filepath.Base(gotPath) != filepath.Base(path) {
		t.Fatalf("Latest(project) = %q, want base %q", gotPath, filepath.Base(path))
	}
	if got.HandoffID != p.HandoffID {
		t.Fatalf("Latest(project) = %q, want %q", gotPath, path)
	}
	_ = store
}

func TestPrepareErrorPaths(t *testing.T) {
	t.Run("uninitialized workspace", func(t *testing.T) {
		if _, _, err := Prepare(t.TempDir(), initRepo(t)); err == nil {
			t.Fatal("Prepare accepted an uninitialized workspace")
		}
	})
	t.Run("repository without commits", func(t *testing.T) {
		dir := t.TempDir()
		initWorkspace(t, dir)
		repo := t.TempDir()
		runGitT(t, repo, "init", "-q")
		_, _, err := Prepare(dir, repo)
		if err == nil || !strings.Contains(err.Error(), "repository has no HEAD commit") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("handoff path is a file", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		writeFileT(t, dir, "handoff", "not a directory")
		if _, _, err := Prepare(dir, repo); err == nil {
			t.Fatal("Prepare accepted a file in place of the handoff directory")
		}
	})
	t.Run("handoff directory is read only", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		hd := filepath.Join(dir, "handoff")
		if err := os.MkdirAll(hd, 0o700); err != nil {
			t.Fatal(err)
		}
		chmodT(t, hd, 0o500)
		if _, _, err := Prepare(dir, repo); err == nil {
			t.Fatal("Prepare wrote into a read-only handoff directory")
		}
	})
	t.Run("events directory unreadable", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		eventsDir := filepath.Join(dir, "events")
		chmodT(t, eventsDir, 0)
		if _, _, err := Prepare(dir, repo); err == nil {
			t.Fatal("Prepare accepted an unreadable events directory")
		}
	})
}

func TestLoadRejectsCorruption(t *testing.T) {
	dir, repo := structuredWorkspace(t)
	_, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); !os.IsNotExist(err) {
			t.Fatalf("missing packet error = %v", err)
		}
	})
	t.Run("corrupt json", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(bad); err == nil {
			t.Fatal("corrupt packet accepted")
		}
	})
	t.Run("missing content hash", func(t *testing.T) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(valid, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "content_hash")
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "nohash.json")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
			t.Fatalf("packet without content hash: %v", err)
		}
	})
	t.Run("tampered field", func(t *testing.T) {
		tampered := strings.Replace(string(valid), `"summary": "`, `"summary": "TAMPERED `, 1)
		p := filepath.Join(t.TempDir(), "tampered.json")
		if err := os.WriteFile(p, []byte(tampered), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
			t.Fatalf("tampered packet: %v", err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		p, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if p.HandoffID == "" || len(p.Tree.Nodes) != 4 {
			t.Fatalf("loaded packet = %+v", p)
		}
	})
}

func TestLatestVariants(t *testing.T) {
	t.Run("no packets", func(t *testing.T) {
		dir := t.TempDir()
		initWorkspace(t, dir)
		if _, _, err := Latest(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("want os.ErrNotExist, got %v", err)
		}
	})

	t.Run("resolves plain and falls back past corrupt newest", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		first, firstPath, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		second, secondPath, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := filepath.Glob(filepath.Join(dir, "handoff", "ho_*.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 2 {
			t.Fatalf("packets = %v", paths)
		}
		sort.Strings(paths)
		// Latest walks packet names in descending order, so corrupting the
		// lexicographically largest name must fall back to the other packet.
		newest := paths[len(paths)-1]
		remaining := paths[0]
		if remaining == newest {
			remaining = paths[1]
		}
		if err := os.WriteFile(newest, []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, gotPath, err := Latest(dir)
		if err != nil {
			t.Fatal(err)
		}
		realRemaining, err := filepath.EvalSymlinks(remaining)
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != realRemaining {
			t.Fatalf("fallback returned %q, want %q", gotPath, realRemaining)
		}
		if got.HandoffID != first.HandoffID && got.HandoffID != second.HandoffID {
			t.Fatalf("fallback returned unknown packet %+v", got)
		}
		_ = firstPath
		_ = secondPath
	})

	t.Run("all packets corrupt", func(t *testing.T) {
		dir := t.TempDir()
		initWorkspace(t, dir)
		if err := os.MkdirAll(filepath.Join(dir, "handoff"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "handoff", "ho_bad.json"), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Latest(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("want os.ErrNotExist, got %v", err)
		}
	})

	t.Run("state dir error", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Latest(f); err == nil {
			t.Fatal("Latest accepted a regular file as a workspace")
		}
	})
}

func TestEvaluateVariants(t *testing.T) {
	t.Run("nil packet", func(t *testing.T) {
		dir := t.TempDir()
		initWorkspace(t, dir)
		ev := Evaluate(dir, t.TempDir(), nil)
		if ev.Status != StatusIncomplete || len(ev.Reasons) != 1 ||
			ev.Reasons[0] != "no prepared handoff for the current workspace" || ev.PacketID != "" {
			t.Fatalf("nil packet evaluation = %+v", ev)
		}
	})

	t.Run("clean matching head is complete", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusComplete || len(ev.Reasons) != 0 || ev.PacketID != p.HandoffID {
			t.Fatalf("clean evaluation = %+v", ev)
		}
	})

	t.Run("stale head", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		runGitT(t, repo, "commit", "--allow-empty", "-qm", "drift")
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusStaleCode || len(ev.Reasons) != 1 || ev.Reasons[0] != "branch/HEAD differs from prepared handoff" {
			t.Fatalf("stale head evaluation = %+v", ev)
		}
	})

	t.Run("branch mismatch", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		runGitT(t, repo, "checkout", "-q", "-b", "other-branch")
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusStaleCode || ev.Reasons[0] != "branch/HEAD differs from prepared handoff" {
			t.Fatalf("branch mismatch evaluation = %+v", ev)
		}
	})

	t.Run("detached after prepare", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		runGitT(t, repo, "checkout", "-q", "--detach")
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusStaleCode {
			t.Fatalf("detached evaluation = %+v", ev)
		}
	})

	t.Run("dirty diff after prepare", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		writeFileT(t, repo, "tracked.txt", "drifted\n")
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusStaleCode || ev.Reasons[0] != "working-tree diff differs from prepared handoff" {
			t.Fatalf("dirty diff evaluation = %+v", ev)
		}
	})

	t.Run("dirty at prepare is incomplete", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		writeFileT(t, repo, "tracked.txt", "dirty\n")
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusIncomplete {
			t.Fatalf("dirty prepare evaluation = %+v", ev)
		}
		found := false
		for _, r := range ev.Reasons {
			if r == "prepared handoff is incomplete" {
				found = true
			}
		}
		if !found {
			t.Fatalf("reasons = %v", ev.Reasons)
		}
	})

	t.Run("untracked-only dirt after prepare is incomplete", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		writeFileT(t, repo, "late-untracked.txt", "new\n")
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusIncomplete || len(ev.Reasons) != 1 ||
			ev.Reasons[0] != "uncommitted code has no captured bytes; restore is metadata-only" {
			t.Fatalf("untracked-only evaluation = %+v", ev)
		}
	})

	t.Run("workspace cannot be opened", func(t *testing.T) {
		repo := initRepo(t)
		p := Packet{HandoffID: "ho_x"}
		ev := Evaluate(t.TempDir(), repo, &p)
		if ev.Status != StatusIncomplete || !strings.Contains(ev.Reasons[0], "cannot open workspace:") {
			t.Fatalf("unopenable workspace evaluation = %+v", ev)
		}
	})

	t.Run("events cannot be loaded", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		chmodT(t, filepath.Join(dir, "events"), 0)
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusIncomplete || !strings.Contains(ev.Reasons[0], "cannot load events:") {
			t.Fatalf("unloadable events evaluation = %+v", ev)
		}
	})

	t.Run("code state failure", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		notRepo := t.TempDir()
		runGitT(t, notRepo, "init", "-q")
		ev := Evaluate(dir, notRepo, &p)
		if ev.Status != StatusIncomplete || len(ev.Reasons) != 1 || !strings.Contains(ev.Reasons[0], "repository has no HEAD commit") {
			t.Fatalf("code state failure evaluation = %+v", ev)
		}
	})

	t.Run("projection incomplete", func(t *testing.T) {
		dir, repo := structuredWorkspace(t)
		writePendingBatchT(t, dir)
		p, _, err := Prepare(dir, repo)
		if err != nil {
			t.Fatal(err)
		}
		ev := Evaluate(dir, repo, &p)
		if ev.Status != StatusIncomplete {
			t.Fatalf("projection-incomplete evaluation = %+v", ev)
		}
		joined := strings.Join(ev.Reasons, "|")
		if !strings.Contains(joined, "projection is incomplete") || !strings.Contains(joined, "prepared handoff is incomplete") {
			t.Fatalf("reasons = %v", ev.Reasons)
		}
	})
}

func TestPreparePrefersActiveGoalAndCountsLiveProvenance(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	initWorkspace(t, dir)

	g1c := appendOwnership(t, dir, "g1", "lg")
	g1Created := appendNode(t, dir, "g1", "goal", "old abandoned goal", "", "lg",
		core.NodePayload{NorthStar: "old star", Provenance: string(core.ProvenanceImported)}, g1c)
	appendStatusT(t, dir, "g1", core.StatusAbandoned, 1, "lg", g1Created)

	g2c := appendOwnership(t, dir, "g2", "lg2")
	appendNode(t, dir, "g2", "goal", "active goal", "", "lg2",
		core.NodePayload{NorthStar: "new star", Provenance: string(core.ProvenanceAgentInferred)}, g2c)

	tc := appendOwnership(t, dir, "t1", "lt")
	tCreated := appendNode(t, dir, "t1", "task", "dropped task", "g2", "lt",
		core.NodePayload{Provenance: string(core.ProvenanceImported)}, tc)
	appendTombstoneT(t, dir, "t1", 1, "lt", tCreated)

	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.Goal == nil || p.Goal.ID != "g2" || p.Goal.Status != string(core.StatusInProgress) ||
		p.Goal.NorthStar != "new star" {
		t.Fatalf("packet must present the active goal, got %+v", p.Goal)
	}
	if len(p.Tree.GoalIDs) != 2 {
		t.Fatalf("goal ids = %v (tree keeps non-deleted goals)", p.Tree.GoalIDs)
	}
	if p.ProvenanceSummary[string(core.ProvenanceImported)] != 1 {
		t.Fatalf("tombstoned node counted in provenance: %+v", p.ProvenanceSummary)
	}
	if p.ProvenanceSummary[string(core.ProvenanceAgentInferred)] != 1 {
		t.Fatalf("provenance = %+v", p.ProvenanceSummary)
	}
	if _, ok := p.ProvenanceSummary["unspecified"]; ok {
		t.Fatalf("unexpected unspecified provenance: %+v", p.ProvenanceSummary)
	}
}
