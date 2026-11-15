package main

// Flow-level CLI tests: every subcommand surface, the exit-code contract, JSON
// output contracts, and the error matrix. These complement cli_test.go (which
// covers the deep state-machine behaviour) by making sure each command is
// actually reachable and fails in a predictable way.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCLIUsageAndExitCodeContract pins the exit-code contract: 0 success,
// 1 runtime/die, 2 usage.
func TestCLIUsageAndExitCodeContract(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"no args", nil, 2},
		{"unknown command", []string{"bogus"}, 2},
		{"node without subcommand", []string{"node"}, 2},
		{"node unknown subcommand", []string{"node", "bogus"}, 2},
		{"event without subcommand", []string{"event"}, 2},
		{"event unknown subcommand", []string{"event", "bogus"}, 2},
		{"evidence without subcommand", []string{"evidence"}, 2},
		{"ownership unknown subcommand", []string{"ownership", "claim"}, 2},
		{"handoff without subcommand", []string{"handoff"}, 2},
		{"handoff unknown subcommand", []string{"handoff", "bogus"}, 2},
		{"adapter without subcommand", []string{"adapter"}, 2},
		{"adapter unknown subcommand", []string{"adapter", "bogus"}, 2},
		{"note unknown subcommand", []string{"note", "bogus"}, 2},
		{"status on uninitialized dir", []string{"status", "--dir", root, "--repo", root}, 1},
		{"doctor on uninitialized dir", []string{"doctor", "--dir", root, "--repo", root}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code := runCLI(t, root, tc.args...)
			if code != tc.code {
				t.Fatalf("%v exit code = %d, want %d", tc.args, code, tc.code)
			}
		})
	}
}

// TestCLIHelpListsEverySurface keeps usage documentation in sync with the
// dispatch table.
func TestCLIHelpListsEverySurface(t *testing.T) {
	root := t.TempDir()
	out, stderr, code := runCLI(t, root, "help")
	if code != 0 {
		t.Fatalf("help exit=%d stderr=%s", code, stderr)
	}
	want := []string{
		"anchor init", "anchor event append", "anchor event audit",
		"anchor node create", "anchor node edit", "anchor node tombstone", "anchor node transition",
		"anchor note set", "anchor evidence add", "anchor evidence audit",
		"anchor ownership takeover", "anchor handoff prepare", "anchor handoff restore",
		"anchor handoff export", "anchor handoff import", "anchor handoff status",
		"anchor adapter capabilities", "anchor adapter render", "anchor adapter capture", "anchor adapter ingest",
		"anchor status", "anchor doctor",
	}
	for _, w := range want {
		if !strings.Contains(out+stderr, w) {
			t.Fatalf("help output is missing %q:\n%s", w, out+stderr)
		}
	}
	if !strings.Contains(out+stderr, "--attach") {
		t.Fatalf("help output does not document --attach:\n%s", out+stderr)
	}
	if !strings.Contains(out+stderr, "--north-star") || !strings.Contains(out+stderr, "--provenance") {
		t.Fatalf("help output does not document the detail-field flags:\n%s", out+stderr)
	}
}

func TestCLIInitArgumentErrors(t *testing.T) {
	root := t.TempDir()
	if _, stderr, code := runCLI(t, root, "init", "--dir", filepath.Join(root, "ws")); code != 1 {
		t.Fatalf("init without --workspace/--device: code=%d stderr=%s", code, stderr)
	}
	ws := filepath.Join(root, "ws")
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "w1", "--device", "d1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	// Re-initializing an existing workspace must fail closed and keep data.
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "w2", "--device", "d2"); code != 1 {
		t.Fatalf("re-init accepted: %s", stderr)
	}
	// Attach to a regular file must fail.
	file := writeFixture(t, filepath.Join(root, "notadir"), "x")
	if _, _, code := runCLI(t, root, "init", "--attach", "--dir", file, "--workspace", "w3", "--device", "d3"); code != 1 {
		t.Fatal("attach to a regular file was accepted")
	}
}

// TestCLIEventAppendAndAudit covers the raw event surface end to end.
func TestCLIEventAppendAndAudit(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	raw := writeFixture(t, filepath.Join(root, "ev.json"), `{"type":"node_created","causal_parents":[],"payload":{"node_id":"raw1","type":"goal","title":"Raw","owner":"agent"}}`)
	out, stderr, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", raw)
	if code != 0 {
		t.Fatalf("event append: %s%s", out, stderr)
	}
	if !strings.Contains(out, "created=true") || !strings.Contains(out, "node_created") {
		t.Fatalf("unexpected append output: %s", out)
	}
	if files := eventFiles(t, ws); len(files) != 1 {
		t.Fatalf("expected one persisted event, got %d", len(files))
	}
	// Re-appending the same logical node must be rejected, not silently applied.
	if _, _, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", raw); code != 1 {
		t.Fatal("duplicate event append was accepted")
	}
	// Malformed JSON and missing file are runtime errors.
	bad := writeFixture(t, filepath.Join(root, "bad.json"), "not-json")
	if _, _, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", bad); code != 1 {
		t.Fatal("malformed event JSON was accepted")
	}
	if _, _, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", filepath.Join(root, "absent.json")); code != 1 {
		t.Fatal("missing event file was accepted")
	}
	// Audit is read-only and reports the batch.
	before := eventFiles(t, ws)
	auditOut, stderr, code := runCLI(t, root, "event", "audit", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("event audit: %s", stderr)
	}
	var audit struct {
		ReadOnly        bool `json:"read_only"`
		Writes          bool `json:"writes"`
		MigrationDryRun bool `json:"migration_dry_run_only"`
		EventLogVersion int  `json:"event_log_version"`
		Files           []struct {
			File   string `json:"file"`
			Format string `json:"format"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(auditOut), &audit); err != nil {
		t.Fatalf("audit JSON: %v: %s", err, auditOut)
	}
	if !audit.ReadOnly || audit.Writes || !audit.MigrationDryRun || audit.EventLogVersion != 2 || len(audit.Files) == 0 {
		t.Fatalf("audit contract broken: %s", auditOut)
	}
	if after := eventFiles(t, ws); len(after) != len(before) {
		t.Fatalf("event audit mutated the log: %d -> %d", len(before), len(after))
	}
}

func TestCLIOwnershipTakeover(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	out, stderr, code := runCLI(t, root, "ownership", "takeover", "--dir", ws, "--id", "goal1", "--owner", "bob")
	if code != 0 {
		t.Fatalf("takeover: %s%s", out, stderr)
	}
	if !strings.Contains(out, "owner=bob") || !strings.Contains(out, "token=2") {
		t.Fatalf("takeover output does not show the fencing bump: %s", out)
	}
	statusOut, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status: %s", statusOut)
	}
	if !strings.Contains(statusOut, `"owner": "bob"`) {
		t.Fatalf("takeover owner not visible in status: %s", statusOut)
	}
	if _, _, code := runCLI(t, root, "ownership", "takeover", "--dir", ws, "--id", "missing", "--owner", "bob"); code != 1 {
		t.Fatal("takeover of an unknown node was accepted")
	}
	if _, _, code := runCLI(t, root, "ownership", "takeover", "--dir", ws); code != 1 {
		t.Fatal("takeover without --id was accepted")
	}
}

// TestCLINodeRejectionMatrix asserts every invalid node operation fails closed
// and leaves the projection intact.
func TestCLINodeRejectionMatrix(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "note1", "--type", "note", "--parent", "goal1", "--title", "N")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing required flags", []string{"node", "create", "--dir", ws}, "required"},
		{"invalid type", []string{"node", "create", "--dir", ws, "--id", "x", "--type", "bogus", "--title", "X"}, "invalid node type"},
		{"duplicate id", []string{"node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "dup"}, "already exists"},
		{"unknown parent", []string{"node", "create", "--dir", ws, "--id", "x", "--type", "task", "--parent", "nope", "--title", "X"}, "not found"},
		{"non-goal without parent", []string{"node", "create", "--dir", ws, "--id", "x", "--type", "task", "--title", "X"}, "requires --parent"},
		{"illegal transition", []string{"node", "transition", "--dir", ws, "--id", "task1", "--to", "done"}, "illegal transition"},
		{"unknown transition target", []string{"node", "transition", "--dir", ws, "--id", "task1", "--to", "bogus"}, "illegal transition"},
		{"edit without change", []string{"node", "edit", "--dir", ws, "--id", "task1"}, "no changes"},
		{"edit with goal field on task", []string{"node", "edit", "--dir", ws, "--id", "task1", "--north-star", "x"}, "goal fields require a goal node"},
		{"note on non-note", []string{"node", "transition", "--dir", ws, "--id", "goal1", "--to", "done"}, "illegal transition"},
		{"tombstone unknown", []string{"node", "tombstone", "--dir", ws, "--id", "nope"}, "not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(eventFiles(t, ws))
			_, stderr, code := runCLI(t, root, tc.args...)
			if code != 1 {
				t.Fatalf("%v accepted: code=%d stderr=%s", tc.args, code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("unexpected diagnostic for %v: %s", tc.args, stderr)
			}
			if after := len(eventFiles(t, ws)); after != before {
				t.Fatalf("rejected command left events behind: %d -> %d", before, after)
			}
		})
	}

	// Tombstone is terminal: later edits must be rejected.
	if _, stderr, code := runCLI(t, root, "node", "tombstone", "--dir", ws, "--id", "note1"); code != 0 {
		t.Fatalf("tombstone: %s", stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "edit", "--dir", ws, "--id", "note1", "--title", "resurrect"); code != 1 {
		t.Fatalf("edit after tombstone accepted: %s", stderr)
	}
	// A rejected command must not corrupt the projection.
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 || !strings.Contains(out, `"projection_status": "complete"`) {
		t.Fatalf("projection damaged by rejections: %s", out)
	}
}

func TestCLIEvidenceRejectionMatrix(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	good := writeFixture(t, filepath.Join(root, "good.txt"), "evidence body\n")
	secret := writeFixture(t, filepath.Join(root, "secret.txt"), "AKIAIOSFODNN7EXAMPLE\n")
	zeroHash := strings.Repeat("0", 64)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing flags", []string{"evidence", "add", "--dir", ws}, "required"},
		{"unknown node", []string{"evidence", "add", "--dir", ws, "--id", "e1", "--node", "nope", "--kind", "test", "--file", good}, "not found"},
		{"missing file", []string{"evidence", "add", "--dir", ws, "--id", "e2", "--node", "goal1", "--kind", "test", "--file", filepath.Join(root, "absent")}, "no such file"},
		{"wrong sha256", []string{"evidence", "add", "--dir", ws, "--id", "e3", "--node", "goal1", "--kind", "test", "--file", good, "--sha256", zeroHash}, "sha256 mismatch"},
		{"secret content", []string{"evidence", "add", "--dir", ws, "--id", "e4", "--node", "goal1", "--kind", "test", "--file", secret}, "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := runCLI(t, root, tc.args...)
			if code != 1 {
				t.Fatalf("%v accepted: %s", tc.args, stderr)
			}
			if !strings.Contains(strings.ToLower(stderr), strings.ToLower(tc.want)) {
				t.Fatalf("unexpected diagnostic: %s", stderr)
			}
		})
	}
	// A valid add then audit must report exactly one referenced blob.
	if _, stderr, code := runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ok1", "--node", "goal1", "--kind", "test", "--file", good); code != 0 {
		t.Fatalf("valid evidence add failed: %s", stderr)
	}
	out, stderr, code := runCLI(t, root, "evidence", "audit", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("audit: %s", stderr)
	}
	var audit struct {
		ReadOnly   bool     `json:"read_only"`
		Referenced []string `json:"referenced_digests"`
		Blobs      []string `json:"blob_files"`
		Orphans    []string `json:"orphan_blobs"`
		Invalid    []string `json:"invalid_artifacts"`
	}
	if err := json.Unmarshal([]byte(out), &audit); err != nil {
		t.Fatal(err)
	}
	if !audit.ReadOnly || len(audit.Referenced) != 1 || len(audit.Blobs) != 1 || len(audit.Orphans) != 0 || len(audit.Invalid) != 0 {
		t.Fatalf("evidence audit contract broken: %s", out)
	}
}

func TestCLIAdapterSurfaceAndErrors(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")

	capsOut, stderr, code := runCLI(t, root, "adapter", "capabilities", "--json")
	if code != 0 {
		t.Fatalf("capabilities: %s", stderr)
	}
	var caps struct {
		Adapters []struct {
			ID               string   `json:"id"`
			EvidenceGrade    string   `json:"evidence_grade"`
			EndToEndVerified bool     `json:"end_to_end_verified"`
			NativeHookStatus string   `json:"native_hook_status"`
			NotClaimed       []string `json:"not_claimed"`
		} `json:"adapters"`
	}
	if err := json.Unmarshal([]byte(capsOut), &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps.Adapters) != 3 {
		t.Fatalf("expected three adapters, got %d", len(caps.Adapters))
	}
	for _, a := range caps.Adapters {
		if a.EvidenceGrade != "D" || a.EndToEndVerified || a.NativeHookStatus != "unverified" || len(a.NotClaimed) == 0 {
			t.Fatalf("adapter %s overclaims: %+v", a.ID, a)
		}
	}

	// render --print is a dry run and must not write anything.
	out, stderr, code := runCLI(t, root, "adapter", "render", "--adapter", "claude-code", "--print")
	if code != 0 {
		t.Fatalf("render print: %s", stderr)
	}
	var render struct {
		DryRun bool `json:"dry_run"`
		Files  []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &render); err != nil {
		t.Fatal(err)
	}
	if !render.DryRun || len(render.Files) != 2 {
		t.Fatalf("render print contract broken: %s", out)
	}
	// render into a directory writes the two files and refuses a second render.
	dir := filepath.Join(root, "render")
	if _, stderr, code := runCLI(t, root, "adapter", "render", "--adapter", "gemini-cli", "--target", dir); code != 0 {
		t.Fatalf("render write: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "gemini-cli", "anchor-adapter.json")); err != nil {
		t.Fatal("render did not write the manifest")
	}
	if _, _, code := runCLI(t, root, "adapter", "render", "--adapter", "gemini-cli", "--target", dir); code != 1 {
		t.Fatal("second render overwrote the target")
	}
	// Capture from stdin, and the error surfaces.
	if out, stderr, code := runCLIInput(t, root, `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/tmp/c"}`, "adapter", "capture", "--adapter", "claude-code", "--stdin", "--print"); code != 0 {
		t.Fatalf("capture stdin: %s%s", out, stderr)
	}
	if _, stderr, code := runCLIInput(t, root, `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/tmp/c"}`, "adapter", "capture", "--adapter", "bogus", "--stdin", "--print"); code != 1 {
		t.Fatalf("capture with unknown adapter accepted: %s", stderr)
	}
	if _, _, code := runCLI(t, root, "adapter", "render", "--adapter", "bogus", "--print"); code != 1 {
		t.Fatal("render with unknown adapter was accepted")
	}
	if _, _, code := runCLI(t, root, "adapter", "render", "--adapter", "claude-code"); code != 1 {
		t.Fatal("render without --target/--print was accepted")
	}
	if _, _, code := runCLI(t, root, "adapter", "ingest", "--adapter", "claude-code", "--dir", ws); code != 1 {
		t.Fatal("ingest without node/event was accepted")
	}
}

func TestCLIHandoffErrorsAndStatus(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")

	// No packet yet.
	out, stderr, code := runCLI(t, root, "handoff", "status", "--dir", ws, "--repo", repo)
	if code != 0 || !strings.Contains(out, "incomplete") {
		t.Fatalf("handoff status without a packet: code=%d %s %s", code, out, stderr)
	}
	if _, _, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", filepath.Join(root, "absent.json")); code != 1 {
		t.Fatal("restore with a missing packet was accepted")
	}
	if _, _, code := runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", filepath.Join(root, "norepo")); code != 1 {
		t.Fatal("prepare with a non-git repo was accepted")
	}
	if _, stderr, code := runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo); code != 0 {
		t.Fatalf("prepare: %s", stderr)
	}
	// Export refuses an existing target.
	existing := filepath.Join(root, "pkg")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", existing); code != 1 {
		t.Fatal("export overwrote an existing target")
	}
	// Import refuses a missing package.
	if _, _, code := runCLI(t, root, "handoff", "import", "--package", filepath.Join(root, "absent"), "--target", filepath.Join(root, "t1")); code != 1 {
		t.Fatal("import of a missing package was accepted")
	}
	// Restore reports a full evaluation for the prepared packet.
	pkt, err := filepath.Glob(filepath.Join(ws, "handoff", "ho_*.json"))
	if err != nil || len(pkt) == 0 {
		t.Fatalf("packet files: %v %v", pkt, err)
	}
	sort.Strings(pkt)
	restoreOut, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0])
	if code != 1 {
		t.Fatalf("pending restore should exit 1: code=%d %s%s", code, restoreOut, stderr)
	}
	var restore struct {
		CodeApply      string `json:"code_apply"`
		RecoveryStatus string `json:"recovery_status"`
		HandoffID      string `json:"handoff_id"`
		CanAck         bool   `json:"can_ack"`
		Progress       struct {
			Acked    bool `json:"acked"`
			Verified bool `json:"verified"`
		} `json:"progress"`
	}
	if err := json.Unmarshal([]byte(restoreOut), &restore); err != nil {
		t.Fatal(err)
	}
	if restore.RecoveryStatus != "complete" || !strings.Contains(restore.CodeApply, "disabled") || restore.HandoffID == "" || !restore.CanAck || restore.Progress.Verified {
		t.Fatalf("restore plan contract broken: %s", restoreOut)
	}
	// Ack then verify the handoff, and the plan becomes verified.
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--ack"); code != 0 {
		t.Fatalf("ack: %s%s", out, stderr)
	}
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--verify"); code != 0 {
		t.Fatalf("verify: %s%s", out, stderr)
	}
	verifiedOut, _, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0])
	if code != 0 {
		t.Fatalf("verified plan should exit 0: %s", verifiedOut)
	}
	if !strings.Contains(verifiedOut, `"verified": true`) {
		t.Fatalf("plan does not show verification: %s", verifiedOut)
	}
}

// TestCLIStatusDoctorJSONContract pins the machine-readable output shape that
// scripts depend on.
func TestCLIStatusDoctorJSONContract(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")

	statusOut, stderr, code := runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json")
	if code != 0 {
		t.Fatalf("status: %s", stderr)
	}
	var status struct {
		WorkspaceDir     string   `json:"workspace_dir"`
		WorkspaceID      string   `json:"workspace_id"`
		DeviceID         string   `json:"device_id"`
		ProjectionStatus string   `json:"projection_status"`
		RecoveryStatus   string   `json:"recovery_status"`
		RecoveryReasons  []string `json:"recovery_reasons"`
		HandoffID        string   `json:"handoff_id"`
		Applied          []string `json:"applied"`
		Pending          []string `json:"pending"`
		Conflicts        []any    `json:"conflicts"`
		Quarantined      []any    `json:"quarantined"`
		Tree             struct {
			Nodes map[string]json.RawMessage `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatal(err)
	}
	if status.WorkspaceDir == "" || status.WorkspaceID != "ws1" || status.DeviceID != "dev1" || status.ProjectionStatus != "complete" ||
		status.RecoveryStatus == "" || len(status.Applied) == 0 || len(status.Tree.Nodes) != 1 {
		t.Fatalf("status JSON contract broken: %s", statusOut)
	}

	doctorOut, stderr, code := runCLI(t, root, "doctor", "--dir", ws, "--repo", repo, "--json")
	if code != 0 {
		t.Fatalf("doctor: %s", stderr)
	}
	var doctor struct {
		WorkspaceDir string `json:"workspace_dir"`
		Checks       []struct {
			Check  string `json:"check"`
			Status string `json:"status"`
		} `json:"checks"`
		ProjectionStatus   string `json:"projection_status"`
		RecoveryStatus     string `json:"recovery_status"`
		RecoveryNotImplied bool   `json:"recovery_is_not_implied_by_projection"`
	}
	if err := json.Unmarshal([]byte(doctorOut), &doctor); err != nil {
		t.Fatal(err)
	}
	if doctor.WorkspaceDir == "" || len(doctor.Checks) != 4 || doctor.Checks[0].Check != "manifest" ||
		doctor.Checks[1].Check != "events" || doctor.Checks[2].Check != "recovery" ||
		doctor.Checks[3].Check != "handoff" || !doctor.RecoveryNotImplied {
		t.Fatalf("doctor JSON contract broken: %s", doctorOut)
	}
}

// TestCLIAttachFullSurfaceFlow drives every command against an attached
// workspace and proves the project tree stays untouched.
func TestCLIAttachFullSurfaceFlow(t *testing.T) {
	project := initRepo(t)
	if _, stderr, code := runCLI(t, project, "init", "--attach", "--dir", project, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("attach: %s", stderr)
	}
	rootEntriesBefore := projectRootEntries(t, project)

	runCLI(t, project, "node", "create", "--dir", project, "--id", "goal1", "--type", "goal", "--title", "G",
		"--north-star", "zero context loss", "--must", "stay local")
	runCLI(t, project, "node", "create", "--dir", project, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	runCLI(t, project, "node", "edit", "--dir", project, "--id", "task1", "--title", "T2")
	runCLI(t, project, "node", "transition", "--dir", project, "--id", "task1", "--to", "in_progress")
	runCLI(t, project, "node", "create", "--dir", project, "--id", "note1", "--type", "note", "--parent", "goal1", "--title", "N")
	runCLI(t, project, "note", "set", "--dir", project, "--id", "note1", "--text", "hello")
	ev := writeFixture(t, filepath.Join(t.TempDir(), "ev.txt"), "evidence body\n")
	runCLI(t, project, "evidence", "add", "--dir", project, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", ev)
	runCLI(t, project, "evidence", "audit", "--dir", project, "--json")
	runCLI(t, project, "ownership", "takeover", "--dir", project, "--id", "task1", "--owner", "bob")
	runCLI(t, project, "handoff", "prepare", "--dir", project, "--repo", project)
	runCLI(t, project, "handoff", "status", "--dir", project, "--repo", project)
	pkt, err := filepath.Glob(filepath.Join(project, ".anchor", "handoff", "ho_*.json"))
	if err != nil || len(pkt) == 0 {
		t.Fatalf("attached packet missing: %v %v", pkt, err)
	}
	runCLI(t, project, "handoff", "restore", "--dir", project, "--repo", project, "--packet", pkt[0])
	runCLI(t, project, "status", "--dir", project, "--repo", project, "--json")
	runCLI(t, project, "doctor", "--dir", project, "--repo", project, "--json")
	pkg := filepath.Join(t.TempDir(), "pkg")
	runCLI(t, project, "handoff", "export", "--dir", project, "--repo", project, "--out", pkg, "--include-bytes")
	restored := filepath.Join(t.TempDir(), "restored")
	if out, stderr, code := runCLI(t, project, "handoff", "import", "--package", pkg, "--target", restored, "--apply", "--json"); code != 0 {
		t.Fatalf("import: %s%s", out, stderr)
	}

	if after := projectRootEntries(t, project); !equalEntries(rootEntriesBefore, after) {
		t.Fatalf("attached commands changed the project tree: %v -> %v", rootEntriesBefore, after)
	}
	// Restored workspace is independent and writable.
	out, stderr, code := runCLI(t, project, "status", "--dir", restored, "--json")
	if code != 0 || !strings.Contains(out, `"goal1"`) {
		t.Fatalf("restored workspace unusable: %s %s", out, stderr)
	}
	if _, stderr, code := runCLI(t, project, "node", "transition", "--dir", restored, "--id", "task1", "--to", "blocked"); code != 0 {
		t.Fatalf("restored workspace not writable: %s", stderr)
	}
}

func projectRootEntries(t *testing.T, project string) []string {
	t.Helper()
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		switch e.Name() {
		case ".git", ".anchor", ".gitignore":
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

func equalEntries(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCLIReinitPreservesState ensures a refused re-init never touches the log.
func TestCLIReinitPreservesState(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	before := eventFiles(t, ws)
	if _, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "ws2", "--device", "dev2"); code != 1 {
		t.Fatalf("re-init accepted: %s", stderr)
	}
	after := eventFiles(t, ws)
	if len(after) != len(before) {
		t.Fatalf("re-init mutated the event log: %d -> %d", len(before), len(after))
	}
	if out, _, code := runCLI(t, root, "status", "--dir", ws, "--json"); code != 0 || !strings.Contains(out, `"projection_status": "complete"`) {
		t.Fatalf("workspace unreadable after refused re-init: %s", out)
	}
}

// TestCLIEndToEndSuccessorFlow is the full local segment as a user would drive
// it: adopt a project, record the task structure, prove a task with evidence,
// snapshot, ship, and resume on a "second device".
func TestCLIEndToEndSuccessorFlow(t *testing.T) {
	project := initRepo(t)
	if out, stderr, code := runCLI(t, project, "init", "--attach", "--dir", project, "--workspace", "ws1", "--device", "laptop"); code != 0 {
		t.Fatalf("attach: %s %s", out, stderr)
	}
	// Keep the project tree clean so the handoff can be complete.
	gitAdd(t, project, ".gitignore")
	gitCommit(t, project, "adopt anchor")

	runCLI(t, project, "node", "create", "--dir", project, "--id", "goal1", "--type", "goal", "--title", "Ship",
		"--north-star", "successor resumes with zero context loss",
		"--must", "keep evidence immutable", "--must-not", "no local causal database",
		"--success", "second device resumes")
	runCLI(t, project, "node", "create", "--dir", project, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "Carry the log")
	runCLI(t, project, "node", "create", "--dir", project, "--id", "dec1", "--type", "decision", "--parent", "goal1", "--title", "Hash packets",
		"--decision", "hash the packet body", "--rationale", "detect tampering")
	ev := writeFixture(t, filepath.Join(t.TempDir(), "test.log"), "all tests passed\n")
	runCLI(t, project, "evidence", "add", "--dir", project, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", ev)

	// The done gate rejects a transition without evidence, then accepts with it.
	if _, stderr, code := runCLI(t, project, "node", "transition", "--dir", project, "--id", "task1", "--to", "in_progress"); code != 0 {
		t.Fatalf("in_progress: %s", stderr)
	}
	if _, stderr, code := runCLI(t, project, "node", "transition", "--dir", project, "--id", "task1", "--to", "done"); code != 1 {
		t.Fatalf("done without evidence was accepted: %s", stderr)
	} else if !strings.Contains(stderr, "done requires --evidence") {
		t.Fatalf("unexpected done-gate diagnostic: %s", stderr)
	}
	if _, stderr, code := runCLI(t, project, "node", "transition", "--dir", project, "--id", "task1", "--to", "done", "--evidence", "ev1"); code != 0 {
		t.Fatalf("done with evidence: %s", stderr)
	}

	// Snapshot, ship, resume.
	runCLI(t, project, "handoff", "prepare", "--dir", project, "--repo", project)
	statusOut, _, code := runCLI(t, project, "status", "--dir", project, "--repo", project, "--json")
	if code != 0 {
		t.Fatalf("status: %s", statusOut)
	}
	if !strings.Contains(statusOut, `"recovery_status": "complete"`) {
		t.Fatalf("clean snapshot should be complete: %s", statusOut)
	}
	pkg := filepath.Join(t.TempDir(), "pkg")
	if out, stderr, code := runCLI(t, project, "handoff", "export", "--dir", project, "--repo", project, "--out", pkg, "--include-bytes", "--json"); code != 0 {
		t.Fatalf("export: %s %s", out, stderr)
	}
	second := filepath.Join(t.TempDir(), "second-device")
	importOut, stderr, code := runCLI(t, project, "handoff", "import", "--package", pkg, "--target", second, "--apply", "--json")
	if code != 0 {
		t.Fatalf("import: %s %s", importOut, stderr)
	}
	var imported struct {
		RecoveryStatus   string `json:"recovery_status"`
		WorkspaceRebuilt bool   `json:"workspace_rebuilt"`
		EventsApplied    int    `json:"events_applied"`
		EvidenceVerified int    `json:"evidence_verified"`
	}
	if err := json.Unmarshal([]byte(importOut), &imported); err != nil {
		t.Fatal(err)
	}
	if !imported.WorkspaceRebuilt || imported.RecoveryStatus != "complete" || imported.EventsApplied == 0 || imported.EvidenceVerified == 0 {
		t.Fatalf("import did not fully rebuild the workspace: %s", importOut)
	}

	// The successor gets the structure it needs, including the completed task.
	resumed, _, code := runCLI(t, project, "status", "--dir", second, "--json")
	if code != 0 {
		t.Fatalf("resumed status: %s", resumed)
	}
	var resumedTree struct {
		Projection string `json:"projection_status"`
		Tree       struct {
			Nodes map[string]struct {
				Status      string   `json:"status"`
				NorthStar   string   `json:"north_star"`
				Must        []string `json:"must"`
				Decision    string   `json:"decision"`
				EvidenceIDs []string `json:"evidence_ids"`
			} `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal([]byte(resumed), &resumedTree); err != nil {
		t.Fatal(err)
	}
	if resumedTree.Projection != "complete" || resumedTree.Tree.Nodes["task1"].Status != "done" {
		t.Fatalf("successor lost the completed task: %s", resumed)
	}
	if resumedTree.Tree.Nodes["goal1"].NorthStar == "" || len(resumedTree.Tree.Nodes["goal1"].Must) != 1 {
		t.Fatalf("successor lost the goal constraints: %s", resumed)
	}
	if resumedTree.Tree.Nodes["dec1"].Decision != "hash the packet body" {
		t.Fatalf("successor lost the decision: %s", resumed)
	}
	// And it can keep working.
	runCLI(t, project, "node", "create", "--dir", second, "--id", "task2", "--type", "task", "--parent", "goal1", "--title", "Continue")
	if _, stderr, code := runCLI(t, project, "node", "transition", "--dir", second, "--id", "task2", "--to", "in_progress"); code != 0 {
		t.Fatalf("successor cannot work: %s", stderr)
	}
}

func gitAdd(t *testing.T, repo string, path string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "add", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v %s", path, err, out)
	}
}

func gitCommit(t *testing.T, repo, msg string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "-c", "user.email=t@example.invalid", "-c", "user.name=Anchor Test", "commit", "-qm", msg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}
}

// TestCLIRawEventCannotClaimUserConfirmation covers the review finding that the
// documented raw-event surface could bypass the provenance guard.
func TestCLIRawEventCannotClaimUserConfirmation(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	ev := writeFixture(t, filepath.Join(root, "uc.json"),
		`{"type":"node_created","causal_parents":[],"actor_attestation":"totally-not-a-user","payload":{"node_id":"g","type":"goal","title":"x","provenance":"user_confirmed"}}`)
	if _, stderr, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", ev); code == 0 {
		t.Fatalf("raw event with self-declared attestation was accepted: %s", stderr)
	}
	out, _, code := runCLI(t, root, "status", "--dir", ws, "--json")
	if code != 0 {
		t.Fatalf("status: %s", out)
	}
	if strings.Contains(out, "user_confirmed") {
		t.Fatalf("user_confirmed leaked into the projection: %s", out)
	}
}

// TestCLIDoctorReportsRequestedDirWhenResolutionFails covers the review finding
// that doctor printed an empty workspace_dir for a half-initialized .anchor.
func TestCLIDoctorReportsRequestedDirWhenResolutionFails(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".anchor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".anchor", "stray.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runCLI(t, project, "doctor", "--dir", project, "--json")
	if code != 1 {
		t.Fatalf("doctor on half-initialized state: code=%d %s", code, out)
	}
	var doc struct {
		WorkspaceDir string `json:"workspace_dir"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.WorkspaceDir == "" {
		t.Fatalf("doctor lost the requested directory: %s", out)
	}
}

// TestCLIAttachMonorepoIgnoresState covers attach into a subdirectory of a Git
// work tree, which has no .git of its own.
func TestCLIAttachMonorepoIgnoresState(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, sub, "init", "--attach", "--dir", sub, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("attach in monorepo: %s", stderr)
	}
	ignore, err := os.ReadFile(filepath.Join(sub, ".gitignore"))
	if err != nil {
		t.Fatalf("monorepo attach did not create a nested .gitignore: %v", err)
	}
	if !strings.Contains(string(ignore), ".anchor/") {
		t.Fatalf("nested .gitignore does not ignore state: %q", ignore)
	}
	status := exec.Command("git", "-C", repo, "status", "--porcelain", "--", sub)
	out, err := status.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ".anchor") {
		t.Fatalf("attached state is visible to the monorepo: %s", out)
	}
}

// TestCLIDuplicateEvidenceBlobRoundTrip covers the review finding that two
// evidence ids sharing one content-addressed blob broke export --include-bytes.
func TestCLIDuplicateEvidenceBlobRoundTrip(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	body := writeFixture(t, filepath.Join(root, "same.log"), "identical evidence\n")
	runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", body)
	runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev2", "--node", "task1", "--kind", "test", "--file", body)
	runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo)
	pkg := filepath.Join(root, "pkg")
	out, stderr, code := runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", pkg, "--include-bytes", "--json")
	if code != 0 {
		t.Fatalf("export with a shared evidence blob failed: %s %s", out, stderr)
	}
	importOut, stderr, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", filepath.Join(root, "restored"), "--apply", "--json")
	if code != 0 {
		t.Fatalf("import: %s %s", importOut, stderr)
	}
	var imp struct {
		EvidenceVerified int `json:"evidence_verified"`
		FilesWritten     int `json:"files_written"`
		Writes           int `json:"writes"`
	}
	if err := json.Unmarshal([]byte(importOut), &imp); err != nil {
		t.Fatal(err)
	}
	if imp.EvidenceVerified != 2 {
		t.Fatalf("expected both evidence ids verified, got %d: %s", imp.EvidenceVerified, importOut)
	}
	if imp.Writes != imp.FilesWritten+1 {
		t.Fatalf("writes should include the import report: %s", importOut)
	}
}

// TestCLIQuarantinedPackageDryRunMatchesApply covers the review finding that a
// dry run reported complete for a package that applies as incomplete.
func TestCLIQuarantinedPackageDryRunMatchesApply(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	if err := os.WriteFile(filepath.Join(ws, "events", "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo)
	pkg := filepath.Join(root, "pkg")
	if out, stderr, code := runCLI(t, root, "handoff", "export", "--dir", ws, "--repo", repo, "--out", pkg, "--json"); code != 0 {
		t.Fatalf("export: %s %s", out, stderr)
	}
	dryOut, _, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", filepath.Join(root, "restored"), "--json")
	if code != 0 {
		t.Fatalf("dry run: %s", dryOut)
	}
	applyOut, _, code := runCLI(t, root, "handoff", "import", "--package", pkg, "--target", filepath.Join(root, "restored"), "--apply", "--json")
	if code != 0 {
		t.Fatalf("apply: %s", applyOut)
	}
	var dry, apply struct {
		RecoveryStatus string `json:"recovery_status"`
	}
	if err := json.Unmarshal([]byte(dryOut), &dry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(applyOut), &apply); err != nil {
		t.Fatal(err)
	}
	if dry.RecoveryStatus != apply.RecoveryStatus {
		t.Fatalf("dry run (%s) disagrees with apply (%s)", dryRecovery(dry), apply.RecoveryStatus)
	}
	if dry.RecoveryStatus != "incomplete" {
		t.Fatalf("quarantined package should be incomplete: %s", dryOut)
	}
}

func dryRecovery(d struct {
	RecoveryStatus string `json:"recovery_status"`
}) string {
	return d.RecoveryStatus
}

// TestCLIPacketPicksActiveGoal covers the review finding that the packet could
// carry an abandoned goal instead of the active one.
func TestCLIPacketPicksActiveGoal(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "g0", "--type", "goal", "--title", "Old", "--north-star", "old star")
	runCLI(t, root, "node", "transition", "--dir", ws, "--id", "g0", "--to", "abandoned")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "g1", "--type", "goal", "--title", "Current", "--north-star", "current star", "--must", "stay local")
	runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo)
	packets, err := filepath.Glob(filepath.Join(ws, "handoff", "ho_*.json"))
	if err != nil || len(packets) == 0 {
		t.Fatalf("packets: %v %v", packets, err)
	}
	sort.Strings(packets)
	b, err := os.ReadFile(packets[len(packets)-1])
	if err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Goal struct {
			ID        string   `json:"id"`
			NorthStar string   `json:"north_star"`
			Must      []string `json:"must"`
		} `json:"goal"`
	}
	if err := json.Unmarshal(b, &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Goal.ID != "g1" || packet.Goal.NorthStar != "current star" || len(packet.Goal.Must) != 1 {
		t.Fatalf("packet selected the wrong goal: %+v", packet.Goal)
	}
}

// TestCLIRestoreSuccessorGateFlow covers the successor gate end to end: a
// prepared handoff blocks mutations until the successor acks AND verifies it.
func TestCLIRestoreSuccessorGateFlow(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "note1", "--type", "note", "--parent", "goal1", "--title", "N")
	if _, stderr, code := runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo); code != 0 {
		t.Fatalf("prepare: %s", stderr)
	}
	pkt, err := filepath.Glob(filepath.Join(ws, "handoff", "ho_*.json"))
	if err != nil || len(pkt) != 1 {
		t.Fatalf("packets: %v %v", pkt, err)
	}

	// Every mutation surface is gated while the handoff is pending.
	pending := []struct {
		name string
		args []string
	}{
		{"node create", []string{"node", "create", "--dir", ws, "--id", "task2", "--type", "task", "--parent", "goal1", "--title", "blocked"}},
		{"node edit", []string{"node", "edit", "--dir", ws, "--id", "task1", "--title", "blocked"}},
		{"node transition", []string{"node", "transition", "--dir", ws, "--id", "task1", "--to", "in_progress"}},
		{"node tombstone", []string{"node", "tombstone", "--dir", ws, "--id", "note1"}},
		{"note set", []string{"note", "set", "--dir", ws, "--id", "note1", "--text", "blocked"}},
		{"ownership takeover", []string{"ownership", "takeover", "--dir", ws, "--id", "task1", "--owner", "someone"}},
	}
	for _, tc := range pending {
		t.Run("gated/"+tc.name, func(t *testing.T) {
			_, stderr, code := runCLI(t, root, tc.args...)
			if code != 1 {
				t.Fatalf("%s was allowed while a handoff is pending: %s", tc.name, stderr)
			}
			if !strings.Contains(stderr, "pending successor verification") || !strings.Contains(stderr, "--ack") {
				t.Fatalf("%s diagnostic not actionable: %s", tc.name, stderr)
			}
		})
	}
	// A raw mutation event cannot bypass the gate either.
	raw := writeFixture(t, filepath.Join(root, "raw.json"),
		`{"type":"node_created","causal_parents":[],"payload":{"node_id":"raw1","type":"goal","title":"raw"}}`)
	if _, stderr, code := runCLI(t, root, "event", "append", "--dir", ws, "--file", raw); code == 0 {
		t.Fatalf("raw mutation event bypassed the gate: %s", stderr)
	}

	// The plan says ack is possible; status exposes the pending gate.
	planOut, _, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0])
	if code != 1 || !strings.Contains(planOut, `"can_ack": true`) {
		t.Fatalf("plan should be pending but ackable: code=%d %s", code, planOut)
	}
	statusOut, _, _ := runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json")
	if !strings.Contains(statusOut, `"pending": true`) || !strings.Contains(statusOut, `"handoff_id": "ho_`) {
		t.Fatalf("status does not expose the gate: %s", statusOut)
	}

	// Ack alone keeps the gate closed; verification opens it.
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--ack"); code != 0 {
		t.Fatalf("ack: %s%s", out, stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task2", "--type", "task", "--parent", "goal1", "--title", "still blocked"); code != 1 {
		t.Fatalf("mutation allowed after ack but before verify: %s", stderr)
	} else if !strings.Contains(stderr, "--verify") {
		t.Fatalf("diagnostic should point at --verify: %s", stderr)
	}
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--verify"); code != 0 {
		t.Fatalf("verify: %s%s", out, stderr)
	}
	if out, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task2", "--type", "task", "--parent", "goal1", "--title", "allowed"); code != 0 {
		t.Fatalf("mutation still blocked after verification: %s%s", out, stderr)
	}
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0]); code != 0 || !strings.Contains(out, `"verified": true`) {
		t.Fatalf("verified plan should exit 0: code=%d %s%s", code, out, stderr)
	}
	// Duplicate transitions are refused with an actionable message.
	if _, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--ack"); code != 1 {
		t.Fatalf("duplicate ack accepted: %s", stderr)
	} else if !strings.Contains(stderr, "already acked") {
		t.Fatalf("unexpected duplicate-ack diagnostic: %s", stderr)
	}
	if out, _, code := runCLI(t, root, "doctor", "--dir", ws, "--repo", repo, "--json"); code != 0 || !strings.Contains(out, `"check": "handoff"`) {
		t.Fatalf("doctor should report the handoff check: %s", out)
	}
}

// TestCLIRestoreStaleCodeRequiresForce covers acking a handoff whose code state
// no longer matches: it must fail closed unless --force is recorded.
func TestCLIRestoreStaleCodeRequiresForce(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo)
	pkt, err := filepath.Glob(filepath.Join(ws, "handoff", "ho_*.json"))
	if err != nil || len(pkt) != 1 {
		t.Fatalf("packets: %v %v", pkt, err)
	}
	// Drift the tracked file so the packet is stale.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planOut, _, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0])
	if code != 1 || !strings.Contains(planOut, `"recovery_status": "stale_code"`) {
		t.Fatalf("plan should be stale_code: code=%d %s", code, planOut)
	}
	if !strings.Contains(planOut, `"kind": "restore_worktree"`) {
		t.Fatalf("plan should suggest restoring the worktree: %s", planOut)
	}
	if _, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--ack"); code != 1 {
		t.Fatalf("stale ack accepted without --force: %s", stderr)
	} else if !strings.Contains(stderr, "force") {
		t.Fatalf("stale ack diagnostic should mention --force: %s", stderr)
	}
	out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--ack", "--force")
	if code != 0 {
		t.Fatalf("forced ack failed: %s%s", out, stderr)
	}
	if !strings.Contains(out, `"forced": true`) {
		t.Fatalf("forced ack was not recorded as forced: %s", out)
	}
	// The gate still requires verification, which the dirty tree permits here.
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--verify"); code != 0 {
		t.Fatalf("verify after forced ack: %s%s", out, stderr)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "allowed"); code != 0 {
		t.Fatalf("workspace still gated after a forced ack+verify: %s", stderr)
	}
}

// TestCLIRestoreVerifyValidatesEvidence covers evidence validation on verify.
func TestCLIRestoreVerifyValidatesEvidence(t *testing.T) {
	root := t.TempDir()
	repo := initRepo(t)
	ws := filepath.Join(root, "ws")
	runCLI(t, root, "init", "--dir", ws, "--workspace", "ws1", "--device", "dev1")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "goal1", "--type", "goal", "--title", "G")
	runCLI(t, root, "node", "create", "--dir", ws, "--id", "task1", "--type", "task", "--parent", "goal1", "--title", "T")
	ev := writeFixture(t, filepath.Join(root, "prooflog.txt"), "tests passed\n")
	runCLI(t, root, "evidence", "add", "--dir", ws, "--id", "ev1", "--node", "task1", "--kind", "test", "--file", ev)
	runCLI(t, root, "handoff", "prepare", "--dir", ws, "--repo", repo)
	pkt, err := filepath.Glob(filepath.Join(ws, "handoff", "ho_*.json"))
	if err != nil || len(pkt) != 1 {
		t.Fatalf("packets: %v %v", pkt, err)
	}
	runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--ack")
	// Unknown evidence is refused before an event is recorded.
	if _, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--verify", "--evidence", "ghost"); code != 1 {
		t.Fatalf("verify accepted unknown evidence: %s", stderr)
	} else if !strings.Contains(stderr, "unknown") {
		t.Fatalf("unexpected diagnostic: %s", stderr)
	}
	// Verifying a real artifact records the successor's evidence ids.
	if out, stderr, code := runCLI(t, root, "handoff", "restore", "--dir", ws, "--repo", repo, "--packet", pkt[0], "--verify", "--evidence", "ev1"); code != 0 {
		t.Fatalf("verify with evidence: %s%s", out, stderr)
	}
	statusOut, _, _ := runCLI(t, root, "status", "--dir", ws, "--repo", repo, "--json")
	if !strings.Contains(statusOut, `"verified": true`) || !strings.Contains(statusOut, `"ev1"`) {
		t.Fatalf("status does not record the verified evidence: %s", statusOut)
	}
	// A blob that disappears invalidates the projection, so the workspace refuses
	// to record further events at all (fail-closed) rather than verifying blind.
	blobs, err := filepath.Glob(filepath.Join(ws, "evidence", "blobs", "*"))
	if err != nil || len(blobs) != 1 {
		t.Fatalf("blobs: %v %v", blobs, err)
	}
	if err := os.Remove(blobs[0]); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", ws, "--id", "task2", "--type", "task", "--parent", "goal1", "--title", "x"); code != 1 {
		t.Fatalf("write succeeded with a missing evidence blob: %s", stderr)
	} else if !strings.Contains(stderr, "pending successor verification") && !strings.Contains(stderr, "incomplete projection") {
		t.Fatalf("unexpected fail-closed diagnostic: %s", stderr)
	}
}
