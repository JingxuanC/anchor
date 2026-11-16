package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

// ---------------------------------------------------------------------------
// test fixtures
// ---------------------------------------------------------------------------

// seededWorkspace is a small, valid workspace with a goal node (g1) and a child
// task node (t1).
type seededWorkspace struct {
	dir         string
	goalEventID string
	taskEventID string
	goalLease   string
	taskLease   string
}

func initWorkspaceT(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := core.InitWorkspace(dir, "ws-test", "dev-test"); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	return dir
}

func appendOwnershipT(t *testing.T, dir, nodeID, lease string, parents ...string) string {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	payload, err := json.Marshal(core.OwnershipPayload{NodeID: nodeID, Owner: "agent", LeaseID: lease, FencingToken: 1})
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := store.Append(core.Event{Type: core.EvOwnershipClaimed, CausalParents: parents, LeaseID: lease, FencingToken: 1, Payload: payload})
	if err != nil {
		t.Fatalf("append ownership claim: %v", err)
	}
	return e.EventID
}

func appendNodeT(t *testing.T, dir, nodeID, nodeType, title, parentID, lease string, payload core.NodePayload, parents ...string) string {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	payload.NodeID = nodeID
	payload.Type = nodeType
	payload.Title = title
	payload.ParentID = parentID
	payload.Owner = "agent"
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := store.Append(core.Event{Type: core.EvNodeCreated, CausalParents: parents, LeaseID: lease, FencingToken: 1, Payload: b})
	if err != nil {
		t.Fatalf("append node %s: %v", nodeID, err)
	}
	return e.EventID
}

func appendTombstoneT(t *testing.T, dir, nodeID string, baseRevision int, lease string, parents ...string) {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	b, err := json.Marshal(core.NodePayload{NodeID: nodeID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(core.Event{
		Type: core.EvNodeTombstoned, CausalParents: parents,
		BaseRevision: baseRevision, ExpectedRevision: baseRevision + 1,
		LeaseID: lease, FencingToken: 1, Payload: b,
	}); err != nil {
		t.Fatalf("append tombstone: %v", err)
	}
}

// appendEvidenceT stores a deterministic artifact and registers it on nodeID.
func appendEvidenceT(t *testing.T, dir, nodeID, evidenceID string) core.EvidenceEntry {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	src := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(src, []byte("deterministic evidence bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := store.PutEvidenceFile(src, "", []string{evidenceID, nodeID, "test"})
	if err != nil {
		t.Fatalf("put evidence file: %v", err)
	}
	payload, err := json.Marshal(core.EvidencePayload{
		EvidenceID: evidenceID, NodeID: nodeID, Kind: "test",
		SHA256: artifact.SHA256, Size: artifact.Size, ArtifactSchema: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(core.Event{Type: core.EvEvidenceAdded, CausalParents: []string{}, Payload: payload}); err != nil {
		t.Fatalf("append evidence event: %v", err)
	}
	return core.EvidenceEntry{
		EvidenceID: evidenceID, NodeID: nodeID, Kind: "test",
		SHA256: artifact.SHA256, Size: artifact.Size, ArtifactSchema: 1,
	}
}

func seedWorkspace(t *testing.T) seededWorkspace {
	t.Helper()
	dir := initWorkspaceT(t)
	goalLease := "lg"
	goalClaim := appendOwnershipT(t, dir, "g1", goalLease)
	goalEvent := appendNodeT(t, dir, "g1", "goal", "Goal", "", goalLease, core.NodePayload{
		NorthStar:  "successor resumes with zero context loss",
		Provenance: string(core.ProvenanceAgentInferred),
	}, goalClaim)
	taskLease := "lt"
	taskClaim := appendOwnershipT(t, dir, "t1", taskLease, goalClaim)
	taskEvent := appendNodeT(t, dir, "t1", "task", "Task", "g1", taskLease, core.NodePayload{
		Provenance: string(core.ProvenanceImported),
	}, taskClaim)
	return seededWorkspace{
		dir:         dir,
		goalEventID: goalEvent,
		taskEventID: taskEvent,
		goalLease:   goalLease,
		taskLease:   taskLease,
	}
}

// preparedFixture is a seeded workspace with evidence and a prepared handoff
// packet in a clean temporary git repository.
type preparedFixture struct {
	dir        string
	repo       string
	packet     handoff.Packet
	packetPath string
	evidenceID string
}

func initRepoT(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Anchor Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-qm", "initial")
	return repo
}

func newPreparedFixture(t *testing.T) preparedFixture {
	t.Helper()
	seed := seedWorkspace(t)
	entry := appendEvidenceT(t, seed.dir, "t1", "ev1")
	repo := initRepoT(t)
	p, path, err := handoff.Prepare(seed.dir, repo)
	if err != nil {
		t.Fatalf("prepare handoff: %v", err)
	}
	return preparedFixture{dir: seed.dir, repo: repo, packet: p, packetPath: path, evidenceID: entry.EvidenceID}
}

// ---------------------------------------------------------------------------
// tool invocation helpers
// ---------------------------------------------------------------------------

func invokeTool(t *testing.T, workspaceDir, name string, args map[string]any) map[string]any {
	t.Helper()
	resp := oneResponse(t, workspaceDir, rpcLine(t, 42, "tools/call", map[string]any{"name": name, "arguments": args}))
	if _, hasError := resp["error"]; hasError {
		t.Fatalf("tools/call %s returned a protocol error: %v", name, resp)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s returned no result object: %v", name, resp)
	}
	return result
}

func toolText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("tool result content = %v, want one entry", result["content"])
	}
	item, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("tool content entry is not an object: %v", content[0])
	}
	if item["type"] != "text" {
		t.Fatalf("tool content type = %v, want text", item["type"])
	}
	text, ok := item["text"].(string)
	if !ok {
		t.Fatalf("tool content text is not a string: %v", item["text"])
	}
	return text
}

func toolIsError(t *testing.T, result map[string]any) bool {
	t.Helper()
	isErr, _ := result["isError"].(bool)
	return isErr
}

func decodeToolOutput(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(toolText(t, result)), &out); err != nil {
		t.Fatalf("tool text is not JSON: %v: %q", err, toolText(t, result))
	}
	return out
}

func requireToolSuccess(t *testing.T, result map[string]any) {
	t.Helper()
	if toolIsError(t, result) {
		t.Fatalf("tool failed: %s", toolText(t, result))
	}
}

// ---------------------------------------------------------------------------
// anchor_status
// ---------------------------------------------------------------------------

func TestStatusToolWithoutRepo(t *testing.T) {
	seed := seedWorkspace(t)
	result := invokeTool(t, seed.dir, "anchor_status", map[string]any{})
	requireToolSuccess(t, result)
	out := decodeToolOutput(t, result)

	if out["workspace_id"] != "ws-test" {
		t.Fatalf("workspace_id = %v", out["workspace_id"])
	}
	if out["device_id"] != "dev-test" {
		t.Fatalf("device_id = %v", out["device_id"])
	}
	if out["projection_status"] != "complete" {
		t.Fatalf("projection_status = %v", out["projection_status"])
	}
	if out["recovery_status"] != "complete" {
		t.Fatalf("recovery_status = %v", out["recovery_status"])
	}
	reasons, ok := out["recovery_reasons"].([]any)
	if !ok {
		t.Fatalf("recovery_reasons = %v, want array", out["recovery_reasons"])
	}
	if len(reasons) == 0 {
		t.Fatalf("recovery_reasons should explain that no repo was provided")
	}
	handoffOut, ok := out["handoff"].(map[string]any)
	if !ok {
		t.Fatalf("handoff = %v, want object", out["handoff"])
	}
	for _, key := range []string{"pending", "handoff_id", "acked", "verified", "packet_missing", "forced"} {
		if _, ok := handoffOut[key]; !ok {
			t.Fatalf("handoff gate is missing %q: %v", key, handoffOut)
		}
	}
	if handoffOut["pending"] != false || handoffOut["acked"] != false || handoffOut["verified"] != false {
		t.Fatalf("unexpected handoff gate: %v", handoffOut)
	}
	if handoffOut["handoff_id"] != "" {
		t.Fatalf("handoff_id = %v, want empty", handoffOut["handoff_id"])
	}
	tree, ok := out["tree"].(map[string]any)
	if !ok {
		t.Fatalf("tree = %v, want object", out["tree"])
	}
	for key, want := range map[string]float64{"nodes": 2, "evidence": 0, "sessions": 0} {
		if got, _ := tree[key].(float64); got != want {
			t.Fatalf("tree.%s = %v, want %v", key, tree[key], want)
		}
	}
	if _, ok := out["evaluation"]; ok {
		t.Fatalf("evaluation must be omitted when repo is not provided")
	}
	assertReplayCounts(t, seed.dir, out)
}

func TestStatusToolWithRepo(t *testing.T) {
	fixture := newPreparedFixture(t)
	result := invokeTool(t, fixture.dir, "anchor_status", map[string]any{"repo": fixture.repo})
	requireToolSuccess(t, result)
	out := decodeToolOutput(t, result)

	if out["recovery_status"] != handoff.StatusComplete {
		t.Fatalf("recovery_status = %v, reasons %v", out["recovery_status"], out["recovery_reasons"])
	}
	evaluation, ok := out["evaluation"].(map[string]any)
	if !ok {
		t.Fatalf("evaluation missing: %v", out)
	}
	if evaluation["recovery_status"] != handoff.StatusComplete {
		t.Fatalf("evaluation = %v", evaluation)
	}
	if evaluation["handoff_id"] != fixture.packet.HandoffID {
		t.Fatalf("evaluation.handoff_id = %v, want %s", evaluation["handoff_id"], fixture.packet.HandoffID)
	}
	handoffOut := out["handoff"].(map[string]any)
	if handoffOut["pending"] != true {
		t.Fatalf("prepared handoff must leave the gate pending: %v", handoffOut)
	}
	if handoffOut["handoff_id"] != fixture.packet.HandoffID {
		t.Fatalf("handoff_id = %v, want %s", handoffOut["handoff_id"], fixture.packet.HandoffID)
	}
	if handoffOut["acked"] != false || handoffOut["verified"] != false {
		t.Fatalf("unexpected progress: %v", handoffOut)
	}
	reasons := out["recovery_reasons"].([]any)
	joined := make([]string, 0, len(reasons))
	for _, r := range reasons {
		joined = append(joined, r.(string))
	}
	if !strings.Contains(strings.Join(joined, "\n"), "pending successor verification") {
		t.Fatalf("recovery_reasons should mention the pending gate: %v", joined)
	}
	tree := out["tree"].(map[string]any)
	if got, _ := tree["evidence"].(float64); got != 1 {
		t.Fatalf("tree.evidence = %v, want 1", tree["evidence"])
	}
	assertReplayCounts(t, fixture.dir, out)
}

// assertReplayCounts verifies the status counts against a direct replay.
func assertReplayCounts(t *testing.T, dir string, out map[string]any) {
	t.Helper()
	store, err := core.Open(dir)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	replay, err := store.LoadReplay()
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got, _ := out["applied"].(float64); got != float64(len(replay.Applied)) {
		t.Fatalf("applied = %v, want %d", out["applied"], len(replay.Applied))
	}
	if got, _ := out["pending"].(float64); got != float64(len(replay.Pending)) {
		t.Fatalf("pending = %v, want %d", out["pending"], len(replay.Pending))
	}
	if got, _ := out["conflicts"].(float64); got != float64(len(replay.Conflicts)) {
		t.Fatalf("conflicts = %v, want %d", out["conflicts"], len(replay.Conflicts))
	}
}

// ---------------------------------------------------------------------------
// anchor_tree
// ---------------------------------------------------------------------------

func TestTreeToolNodesLimitAndDeleted(t *testing.T) {
	seed := seedWorkspace(t)

	result := invokeTool(t, seed.dir, "anchor_tree", map[string]any{})
	requireToolSuccess(t, result)
	out := decodeToolOutput(t, result)
	nodes := out["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2: %v", len(nodes), nodes)
	}
	first := nodes[0].(map[string]any)
	for _, key := range []string{"id", "type", "title", "status", "parent_id", "owner", "provenance", "deleted"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("node is missing %q: %v", key, first)
		}
	}
	if first["id"] != "g1" || first["type"] != "goal" || first["status"] != "in_progress" {
		t.Fatalf("unexpected first node: %v", first)
	}
	if first["provenance"] != string(core.ProvenanceAgentInferred) || first["deleted"] != false {
		t.Fatalf("unexpected first node provenance/deleted: %v", first)
	}
	second := nodes[1].(map[string]any)
	if second["id"] != "t1" || second["parent_id"] != "g1" {
		t.Fatalf("unexpected second node: %v", second)
	}
	if evidence, ok := out["evidence"].([]any); !ok || len(evidence) != 0 {
		t.Fatalf("evidence = %v, want empty array", out["evidence"])
	}
	sessions, ok := out["sessions"].(map[string]any)
	if !ok {
		t.Fatalf("sessions = %v, want object", out["sessions"])
	}
	if count, _ := sessions["count"].(float64); count != 0 {
		t.Fatalf("sessions.count = %v", sessions["count"])
	}
	if ids, ok := sessions["ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("sessions.ids = %v, want empty array", sessions["ids"])
	}

	limited := decodeToolOutput(t, invokeTool(t, seed.dir, "anchor_tree", map[string]any{"limit": 1}))
	if got := len(limited["nodes"].([]any)); got != 1 {
		t.Fatalf("limit=1 returned %d nodes", got)
	}
	clamped := decodeToolOutput(t, invokeTool(t, seed.dir, "anchor_tree", map[string]any{"limit": 5000}))
	if got := len(clamped["nodes"].([]any)); got != 2 {
		t.Fatalf("limit=5000 returned %d nodes, want all 2", got)
	}

	// Tombstone the task node: it disappears by default but is visible on request.
	appendTombstoneT(t, seed.dir, "t1", 1, seed.taskLease, seed.taskEventID)
	defaultOut := decodeToolOutput(t, invokeTool(t, seed.dir, "anchor_tree", map[string]any{}))
	if got := len(defaultOut["nodes"].([]any)); got != 1 {
		t.Fatalf("tombstoned node still visible: got %d nodes", got)
	}
	withDeleted := decodeToolOutput(t, invokeTool(t, seed.dir, "anchor_tree", map[string]any{"include_deleted": true}))
	nodes = withDeleted["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("include_deleted returned %d nodes, want 2", len(nodes))
	}
	var deletedSeen bool
	for _, n := range nodes {
		node := n.(map[string]any)
		if node["id"] == "t1" {
			deletedSeen = node["deleted"] == true
		}
	}
	if !deletedSeen {
		t.Fatalf("t1 should be reported deleted: %v", nodes)
	}
}

func TestTreeToolEvidence(t *testing.T) {
	seed := seedWorkspace(t)
	entry := appendEvidenceT(t, seed.dir, "t1", "ev1")
	out := decodeToolOutput(t, invokeTool(t, seed.dir, "anchor_tree", map[string]any{}))
	evidence := out["evidence"].([]any)
	if len(evidence) != 1 {
		t.Fatalf("evidence = %v, want one entry", out["evidence"])
	}
	got := evidence[0].(map[string]any)
	if got["evidence_id"] != entry.EvidenceID || got["node_id"] != entry.NodeID || got["kind"] != entry.Kind {
		t.Fatalf("evidence entry = %v", got)
	}
	if got["sha256"] != entry.SHA256 {
		t.Fatalf("sha256 = %v, want %s", got["sha256"], entry.SHA256)
	}
	if size, _ := got["size"].(float64); size != float64(entry.Size) {
		t.Fatalf("size = %v, want %d", got["size"], entry.Size)
	}
}

// ---------------------------------------------------------------------------
// anchor_evidence_verify
// ---------------------------------------------------------------------------

func TestEvidenceVerifyTool(t *testing.T) {
	seed := seedWorkspace(t)
	entry := appendEvidenceT(t, seed.dir, "t1", "ev1")

	result := invokeTool(t, seed.dir, "anchor_evidence_verify", map[string]any{"evidence_id": entry.EvidenceID})
	requireToolSuccess(t, result)
	out := decodeToolOutput(t, result)
	if out["evidence_id"] != entry.EvidenceID {
		t.Fatalf("evidence_id = %v", out["evidence_id"])
	}
	if out["verified"] != true {
		t.Fatalf("verified = %v, want true (detail %v)", out["verified"], out["detail"])
	}
	if detail, _ := out["detail"].(string); detail == "" {
		t.Fatalf("detail must not be empty on success")
	}

	missing := invokeTool(t, seed.dir, "anchor_evidence_verify", map[string]any{})
	if !toolIsError(t, missing) {
		t.Fatalf("missing evidence_id must be a tool error: %v", missing)
	}
	if text := toolText(t, missing); !strings.Contains(text, "evidence_id is required") {
		t.Fatalf("missing evidence_id text = %q", text)
	}

	unknown := invokeTool(t, seed.dir, "anchor_evidence_verify", map[string]any{"evidence_id": "ev-missing"})
	if !toolIsError(t, unknown) {
		t.Fatalf("unknown evidence id must be a tool error: %v", unknown)
	}
	if text := toolText(t, unknown); !strings.Contains(text, "unknown evidence") {
		t.Fatalf("unknown evidence id text = %q", text)
	}
}

// ---------------------------------------------------------------------------
// anchor_restore_plan
// ---------------------------------------------------------------------------

func TestRestorePlanToolWithoutPacket(t *testing.T) {
	seed := seedWorkspace(t)
	result := invokeTool(t, seed.dir, "anchor_restore_plan", map[string]any{})
	if !toolIsError(t, result) {
		t.Fatalf("restore plan without a packet must be a tool error: %v", result)
	}
	if text := toolText(t, result); !strings.Contains(text, "no prepared handoff") {
		t.Fatalf("error text = %q", text)
	}
}

func TestRestorePlanToolWithPreparedPacket(t *testing.T) {
	fixture := newPreparedFixture(t)
	result := invokeTool(t, fixture.dir, "anchor_restore_plan", map[string]any{"repo": fixture.repo})
	requireToolSuccess(t, result)
	out := decodeToolOutput(t, result)

	if out["handoff_id"] != fixture.packet.HandoffID {
		t.Fatalf("handoff_id = %v, want %s", out["handoff_id"], fixture.packet.HandoffID)
	}
	if out["packet_path"] != fixture.packetPath {
		t.Fatalf("packet_path = %v, want %s", out["packet_path"], fixture.packetPath)
	}
	if schema, _ := out["schema_version"].(float64); schema != 1 {
		t.Fatalf("schema_version = %v", out["schema_version"])
	}
	if out["recovery_status"] != handoff.StatusComplete {
		t.Fatalf("recovery_status = %v, reasons %v", out["recovery_status"], out["reasons"])
	}
	codeApply, _ := out["code_apply"].(string)
	if !strings.Contains(codeApply, "disabled") {
		t.Fatalf("code_apply = %q, want a disabled description", codeApply)
	}
	code := out["code"].(map[string]any)
	if code["expected_head"] != fixture.packet.CodeState.Head {
		t.Fatalf("code.expected_head = %v", code["expected_head"])
	}
	if code["head_matches"] != true || code["branch_matches"] != true {
		t.Fatalf("code comparison = %v", code)
	}
	progress := out["progress"].(map[string]any)
	if progress["acked"] != false || progress["verified"] != false {
		t.Fatalf("progress = %v", progress)
	}
	if out["can_ack"] != true || out["can_verify"] != false {
		t.Fatalf("can_ack/can_verify = %v/%v", out["can_ack"], out["can_verify"])
	}
	actions := out["actions"].([]any)
	kinds := make([]string, 0, len(actions))
	for _, raw := range actions {
		kinds = append(kinds, raw.(map[string]any)["kind"].(string))
	}
	if !strings.Contains(strings.Join(kinds, ","), "ack") {
		t.Fatalf("actions = %v, want an ack action", kinds)
	}

	// An explicit packet path must produce the same plan.
	explicit := decodeToolOutput(t, invokeTool(t, fixture.dir, "anchor_restore_plan", map[string]any{
		"packet": fixture.packetPath, "repo": fixture.repo,
	}))
	if explicit["handoff_id"] != fixture.packet.HandoffID {
		t.Fatalf("explicit packet plan handoff_id = %v", explicit["handoff_id"])
	}

	// The read-only tool must not record ack/verify events.
	gate, err := handoff.Gate(fixture.dir)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !gate.Pending || gate.Progress.Acked || gate.Progress.Verified {
		t.Fatalf("restore_plan mutated handoff progress: %+v", gate)
	}
}

// ---------------------------------------------------------------------------
// read-only guarantee
// ---------------------------------------------------------------------------

// TestToolsDoNotWriteWorkspace snapshots the workspace (paths, modes, contents)
// around a batch of all four tools and requires a byte-identical result.
func TestToolsDoNotWriteWorkspace(t *testing.T) {
	fixture := newPreparedFixture(t)
	before := snapshotTree(t, fixture.dir)
	if len(before) == 0 {
		t.Fatal("empty workspace snapshot")
	}

	input := rpcLine(t, 1, "tools/call", map[string]any{"name": "anchor_status", "arguments": map[string]any{"repo": fixture.repo}}) +
		rpcLine(t, 2, "tools/call", map[string]any{"name": "anchor_tree", "arguments": map[string]any{}}) +
		rpcLine(t, 3, "tools/call", map[string]any{"name": "anchor_evidence_verify", "arguments": map[string]any{"evidence_id": fixture.evidenceID}}) +
		rpcLine(t, 4, "tools/call", map[string]any{"name": "anchor_restore_plan", "arguments": map[string]any{"repo": fixture.repo}}) +
		rpcLine(t, 5, "tools/call", map[string]any{"name": "anchor_evidence_verify", "arguments": map[string]any{"evidence_id": "missing"}})
	msgs := mustRunServer(t, fixture.dir, input)
	if len(msgs) != 5 {
		t.Fatalf("want 5 responses, got %d", len(msgs))
	}
	for i := 0; i < 4; i++ {
		result, ok := msgs[i]["result"].(map[string]any)
		if !ok {
			t.Fatalf("response %d has no result: %v", i, msgs[i])
		}
		if toolIsError(t, result) {
			t.Fatalf("tool %d failed: %s", i, toolText(t, result))
		}
	}
	lastResult := msgs[4]["result"].(map[string]any)
	if !toolIsError(t, lastResult) {
		t.Fatalf("unknown evidence should be a tool error: %v", lastResult)
	}

	after := snapshotTree(t, fixture.dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("tools modified the workspace:\n%s", diffSnapshots(before, after))
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			snapshot[rel] = "dir:" + info.Mode().Perm().String()
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			snapshot[rel] = "file:" + info.Mode().Perm().String() + ":" + hex.EncodeToString(sum[:])
		default:
			snapshot[rel] = "other:" + info.Mode().String()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot workspace: %v", err)
	}
	return snapshot
}

func diffSnapshots(before, after map[string]string) string {
	var b strings.Builder
	for path, want := range before {
		got, ok := after[path]
		if !ok {
			b.WriteString("removed: " + path + "\n")
		} else if got != want {
			b.WriteString("changed: " + path + "\n")
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			b.WriteString("added: " + path + "\n")
		}
	}
	return b.String()
}

func TestToolsOnUninitializedWorkspace(t *testing.T) {
	empty := t.TempDir()
	for _, name := range []string{"anchor_status", "anchor_tree"} {
		result := invokeTool(t, empty, name, map[string]any{})
		if !toolIsError(t, result) {
			t.Fatalf("%s on uninitialized workspace must be a tool error: %v", name, result)
		}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"anchor_evidence_verify", map[string]any{"evidence_id": "ev1"}},
		{"anchor_restore_plan", map[string]any{}},
	} {
		result := invokeTool(t, empty, tc.name, tc.args)
		if !toolIsError(t, result) {
			t.Fatalf("%s on uninitialized workspace must be a tool error: %v", tc.name, result)
		}
	}
}

func TestRestorePlanToolWithBadPacketPath(t *testing.T) {
	seed := seedWorkspace(t)
	result := invokeTool(t, seed.dir, "anchor_restore_plan", map[string]any{"packet": filepath.Join(t.TempDir(), "missing.json")})
	if !toolIsError(t, result) {
		t.Fatalf("bad packet path must be a tool error: %v", result)
	}
	if text := toolText(t, result); !strings.Contains(text, "cannot load handoff packet") {
		t.Fatalf("error text = %q", text)
	}
}

func TestStatusToolWithNonGitRepo(t *testing.T) {
	seed := seedWorkspace(t)
	notARepo := t.TempDir()
	result := invokeTool(t, seed.dir, "anchor_status", map[string]any{"repo": notARepo})
	requireToolSuccess(t, result)
	out := decodeToolOutput(t, result)
	if out["recovery_status"] != handoff.StatusIncomplete {
		t.Fatalf("recovery_status = %v, want %q", out["recovery_status"], handoff.StatusIncomplete)
	}
	evaluation, ok := out["evaluation"].(map[string]any)
	if !ok {
		t.Fatalf("evaluation missing: %v", out)
	}
	if _, ok := evaluation["reasons"].([]any); !ok {
		t.Fatalf("evaluation.reasons missing: %v", evaluation)
	}
	if reasons, _ := out["recovery_reasons"].([]any); len(reasons) == 0 {
		t.Fatalf("recovery_reasons must explain the failed evaluation")
	}
}
