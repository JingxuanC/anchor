package recovery

// Extra coverage tests for recovery: Export and shared fixture helpers. New
// file; the original recovery_test.go helpers (initGitRepo, sealManifest,
// writeManifest, baseManifest, hashBytes) are reused.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

func runGitR(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func writeFileR(t *testing.T, dir, name, content string) string {
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

func chmodR(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

// newRepoR creates a git repo with one committed file so staged/unstaged
// changes can be produced deterministically.
func newRepoR(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "T"}, {"add", "tracked.txt"},
		{"commit", "-qm", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if args[0] == "add" {
			writeFileR(t, repo, "tracked.txt", "clean\n")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

func newWorkspaceR(t *testing.T, workspaceID, deviceID string) (string, *core.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := core.InitWorkspace(dir, workspaceID, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return dir, store
}

func appendClaimR(t *testing.T, store *core.Store, node, lease string) core.Event {
	t.Helper()
	b, err := json.Marshal(core.OwnershipPayload{NodeID: node, Owner: "agent", LeaseID: lease, FencingToken: 1})
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := store.Append(core.Event{Type: core.EvOwnershipClaimed, LeaseID: lease, FencingToken: 1, Payload: b})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func appendNodeR(t *testing.T, store *core.Store, node, typ, title, parent, lease string, payload core.NodePayload, parents ...string) core.Event {
	t.Helper()
	payload.NodeID = node
	payload.Type = typ
	payload.Title = title
	payload.ParentID = parent
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	e, _, err := store.Append(core.Event{
		Type: core.EvNodeCreated, CausalParents: parents, LeaseID: lease, FencingToken: 1, Payload: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// appendEvidenceR stores bytes through the explicit ingestion boundary and
// registers them with an evidence_added event that descends from parents.
func appendEvidenceR(t *testing.T, store *core.Store, nodeID, evidenceID, content string, parents ...string) core.EvidenceArtifact {
	t.Helper()
	src := writeFileR(t, t.TempDir(), "artifact.txt", content)
	artifact, err := store.PutEvidenceFile(src, "", []string{evidenceID, nodeID, "test"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(core.EvidencePayload{
		EvidenceID: evidenceID, NodeID: nodeID, Kind: "test",
		SHA256: artifact.SHA256, Size: artifact.Size, ArtifactSchema: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(core.Event{Type: core.EvEvidenceAdded, CausalParents: parents, Payload: b}); err != nil {
		t.Fatal(err)
	}
	return artifact
}

// evidenceFixture returns a clean repo and a workspace with one goal, one task
// and one verified evidence artifact, all replaying cleanly.
func evidenceFixture(t *testing.T) (dir, repo string, artifact core.EvidenceArtifact) {
	t.Helper()
	return evidenceFixtureWithRepo(t, initGitRepo(t))
}

func evidenceFixtureWithRepo(t *testing.T, repo string) (dir, repoOut string, artifact core.EvidenceArtifact) {
	t.Helper()
	dir, store := newWorkspaceR(t, "ws-evidence", "dev-evidence")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Ship", "", "lg",
		core.NodePayload{NorthStar: "north star"}, gc.EventID)
	tc := appendClaimR(t, store, "t1", "lt")
	created := appendNodeR(t, store, "t1", string(core.NodeTask), "Carry", "g1", "lt", core.NodePayload{}, tc.EventID)
	artifact = appendEvidenceR(t, store, "t1", "ev1", "clean evidence bytes\n", created.EventID)
	return dir, repo, artifact
}

// pendingBatchR writes a hash-valid batch whose event names a missing causal
// parent, so replay reports an incomplete projection.
func pendingBatchR(t *testing.T, stateDir string) {
	t.Helper()
	ev := core.Event{
		SchemaVersion: core.SchemaVersion,
		EventID:       "pending-evt-1",
		WorkspaceID:   "ws-evidence",
		DeviceID:      "dev-evidence",
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

func exportToPackage(t *testing.T, dir, repo string, includeBytes bool) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pkg")
	_, canonical, err := Export(dir, repo, out, includeBytes)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func readManifestR(t *testing.T, pkg string) Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// verifySealedPackage checks the manifest content hash and every listed file.
func verifySealedPackage(t *testing.T, pkg string) Manifest {
	t.Helper()
	m := readManifestR(t, pkg)
	clone := m
	clone.ContentHash = ""
	body, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentHash != hashBytes(body) {
		t.Fatalf("manifest content hash mismatch: %q", m.ContentHash)
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if seen[f.Path] {
			t.Fatalf("duplicate manifest file %q", f.Path)
		}
		seen[f.Path] = true
		data, err := os.ReadFile(filepath.Join(pkg, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatalf("listed file %q: %v", f.Path, err)
		}
		if int64(len(data)) != f.Size || hashBytes(data) != f.SHA256 {
			t.Fatalf("listed file %q metadata mismatch", f.Path)
		}
	}
	return m
}

func fileEntry(m Manifest, rel string) (FileEntry, bool) {
	for _, f := range m.Files {
		if f.Path == rel {
			return f, true
		}
	}
	return FileEntry{}, false
}

func packageHasPath(m Manifest, rel string) bool {
	_, ok := fileEntry(m, rel)
	return ok
}

func TestExportCarriesCanonicalStateAndSealsManifest(t *testing.T) {
	dir, repo, artifact := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, false)
	m := verifySealedPackage(t, pkg)

	if m.SchemaVersion != SchemaVersion || !strings.HasPrefix(m.ExportID, "exp_") {
		t.Fatalf("manifest identity = %+v", m)
	}
	if m.WorkspaceID != "ws-evidence" || m.DeviceID != "dev-evidence" {
		t.Fatalf("manifest workspace identity = %q/%q", m.WorkspaceID, m.DeviceID)
	}
	if m.EventLogVersion != core.CurrentEventLogVersion {
		t.Fatalf("event log version = %d", m.EventLogVersion)
	}
	if m.CodeApply != "disabled" || m.AdapterNativeRecovery != "unverified" {
		t.Fatalf("manifest flags = %+v", m)
	}
	if m.CodeCommitIncluded {
		t.Fatal("git commit bytes must never be included")
	}
	if m.Packet.HandoffID == "" || m.Packet.ContentHash == "" {
		t.Fatalf("packet not embedded: %+v", m.Packet)
	}
	if !packageHasPath(m, "payload/workspace/manifest.json") {
		t.Fatalf("workspace manifest missing from package: %+v", m.Files)
	}
	if len(m.Files) < 3 {
		t.Fatalf("expected workspace, event and packet files, got %+v", m.Files)
	}
	if m.EventBatches == 0 || m.EventBatches != countEntries(m.Files, "payload/events/") {
		t.Fatalf("event batches = %d (%+v)", m.EventBatches, m.Files)
	}
	for rel := range map[string]bool{"payload/workspace/manifest.json": true} {
		want, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(pkg, filepath.FromSlash(rel)))
		if err != nil || string(got) != string(want) {
			t.Fatalf("workspace manifest bytes not mirrored: %v", err)
		}
	}
	if m.Packet.Tree.ProjectionStatus != "complete" {
		t.Fatalf("projection = %q", m.Packet.Tree.ProjectionStatus)
	}
	// Metadata only: the verified evidence bytes were not transported.
	if m.ArtifactAvailability != "metadata_only" || m.EvidenceBytesIncluded != 0 {
		t.Fatalf("availability = %q evidence=%d", m.ArtifactAvailability, m.EvidenceBytesIncluded)
	}
	if packageHasPath(m, "payload/evidence/"+artifact.SHA256) {
		t.Fatal("evidence bytes were included without includeBytes")
	}
}

func countEntries(files []FileEntry, prefix string) int {
	n := 0
	for _, f := range files {
		if strings.HasPrefix(f.Path, prefix) {
			n++
		}
	}
	return n
}

func TestExportMetadataOnlyWithoutEvidenceIsComplete(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-plain", "dev-plain")
	_ = store
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	m := verifySealedPackage(t, pkg)
	if m.RecoveryStatus != handoff.StatusComplete {
		t.Fatalf("status = %q warnings=%v", m.RecoveryStatus, m.Warnings)
	}
	// The workspace manifest and the recorded handoff_prepared event are always
	// transported (export records preparation, so the successor gate can arm).
	if m.ArtifactAvailability != "metadata_only" || len(m.Files) != 2 {
		t.Fatalf("files = %+v availability=%q", m.Files, m.ArtifactAvailability)
	}
	warn := false
	for _, w := range m.Warnings {
		if strings.Contains(w, "git commit bytes are not included") {
			warn = true
		}
	}
	if !warn {
		t.Fatalf("warnings = %v", m.Warnings)
	}
	if m.Packet.Goal != nil {
		t.Fatalf("empty workspace packet goal = %+v", m.Packet.Goal)
	}
}

func TestExportEvidenceWithoutBytesIsIncomplete(t *testing.T) {
	dir, repo, _ := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, false)
	m := verifySealedPackage(t, pkg)
	if m.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("status = %q", m.RecoveryStatus)
	}
	if m.ArtifactAvailability != "metadata_only" {
		t.Fatalf("availability = %q", m.ArtifactAvailability)
	}
	found := false
	for _, w := range m.Warnings {
		if w == "evidence bytes were not included; evidence cannot be verified after import" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", m.Warnings)
	}
}

func TestExportIncludeBytesCapturesEvidenceAndWorktree(t *testing.T) {
	repo := newRepoR(t)
	dir, _, artifact := evidenceFixtureWithRepo(t, repo)
	// Produce one staged and one unstaged change plus one untracked file.
	writeFileR(t, repo, "tracked.txt", "staged version\n")
	runGitR(t, repo, "add", "tracked.txt")
	writeFileR(t, repo, "tracked.txt", "unstaged version\n")
	writeFileR(t, repo, "untracked.txt", "untracked clean bytes\n")

	pkg := exportToPackage(t, dir, repo, true)
	m := verifySealedPackage(t, pkg)
	if m.ArtifactAvailability != "partial" {
		t.Fatalf("availability = %q", m.ArtifactAvailability)
	}
	if m.EvidenceBytesIncluded != 1 || m.UntrackedBytesIncluded != 1 {
		t.Fatalf("byte counts: evidence=%d untracked=%d", m.EvidenceBytesIncluded, m.UntrackedBytesIncluded)
	}
	if !m.StagedDiffIncluded || !m.UnstagedDiffIncluded {
		t.Fatalf("diff flags: staged=%v unstaged=%v", m.StagedDiffIncluded, m.UnstagedDiffIncluded)
	}
	// Diffs and untracked bytes are transported, but a dirty tree additionally
	// needs the commit bundle to be reproducibly applicable, so this package is
	// honestly incomplete.
	if m.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("dirty export without a code bundle status = %q (%v)", m.RecoveryStatus, m.Warnings)
	}
	// Evidence bytes round-trip exactly.
	evPath := filepath.Join(pkg, "payload", "evidence", artifact.SHA256)
	evBytes, err := os.ReadFile(evPath)
	if err != nil || hashBytes(evBytes) != "sha256:"+artifact.SHA256 {
		t.Fatalf("evidence bytes = %v", err)
	}
	untrackedBytes, err := os.ReadFile(filepath.Join(pkg, "payload", "untracked", "untracked.txt"))
	if err != nil || string(untrackedBytes) != "untracked clean bytes\n" {
		t.Fatalf("untracked bytes = %q %v", untrackedBytes, err)
	}
	staged, err := os.ReadFile(filepath.Join(pkg, "payload", "git", "staged.diff"))
	if err != nil || !strings.Contains(string(staged), "staged version") {
		t.Fatalf("staged diff bytes = %v", err)
	}
	unstaged, err := os.ReadFile(filepath.Join(pkg, "payload", "git", "unstaged.diff"))
	if err != nil || !strings.Contains(string(unstaged), "unstaged version") {
		t.Fatalf("unstaged diff bytes = %v", err)
	}
	if packageHasPath(m, "payload/git/commit.diff") {
		t.Fatal("git commit bytes appeared in the package")
	}
}

func TestExportIncludeBytesCleanEvidenceIsComplete(t *testing.T) {
	dir, repo, artifact := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, true)
	m := verifySealedPackage(t, pkg)
	if m.RecoveryStatus != handoff.StatusComplete {
		t.Fatalf("status = %q warnings=%v", m.RecoveryStatus, m.Warnings)
	}
	if m.ArtifactAvailability != "partial" || m.EvidenceBytesIncluded != 1 {
		t.Fatalf("availability = %q evidence=%d", m.ArtifactAvailability, m.EvidenceBytesIncluded)
	}
	if !packageHasPath(m, "payload/evidence/"+artifact.SHA256) {
		t.Fatalf("evidence file missing: %+v", m.Files)
	}
	if m.StagedDiffIncluded || m.UnstagedDiffIncluded || m.UntrackedBytesIncluded != 0 {
		t.Fatalf("clean export claimed worktree bytes: %+v", m)
	}
}

func TestExportUntrackedUnavailableWarns(t *testing.T) {
	cases := []struct {
		name string
		// prepare returns a repo whose only change is an unavailable untracked file.
		prepare func(t *testing.T) (repo, wantPath string)
	}{
		{
			name: "oversize",
			prepare: func(t *testing.T) (string, string) {
				repo := newRepoR(t)
				p := writeFileR(t, repo, "big.bin", "")
				if err := os.Truncate(p, 10<<20+1); err != nil {
					t.Fatal(err)
				}
				return repo, "big.bin"
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T) (string, string) {
				repo := newRepoR(t)
				target := writeFileR(t, t.TempDir(), "target.txt", "x")
				if err := os.Symlink(target, filepath.Join(repo, "link.txt")); err != nil {
					t.Fatal(err)
				}
				return repo, "link.txt"
			},
		},
		{
			name: "unreadable",
			prepare: func(t *testing.T) (string, string) {
				repo := newRepoR(t)
				p := writeFileR(t, repo, "unreadable.txt", "clean\n")
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
				if _, err := os.ReadFile(p); err == nil {
					t.Skipf("cannot make a file unreadable for uid %d", os.Geteuid())
				}
				return repo, "unreadable.txt"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, wantPath := tc.prepare(t)
			dir, _ := newWorkspaceR(t, "ws-untracked", "dev-untracked")
			pkg := exportToPackage(t, dir, repo, true)
			m := verifySealedPackage(t, pkg)
			if m.ArtifactAvailability != "partial" || m.RecoveryStatus != handoff.StatusIncomplete {
				t.Fatalf("availability = %q status = %q", m.ArtifactAvailability, m.RecoveryStatus)
			}
			if m.UntrackedBytesIncluded != 0 {
				t.Fatalf("untracked bytes included = %d", m.UntrackedBytesIncluded)
			}
			want := "untracked bytes unavailable: " + wantPath
			found := false
			for _, w := range m.Warnings {
				if w == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("warnings = %v, want %q", m.Warnings, want)
			}
			found = false
			for _, w := range m.Warnings {
				if w == "some selected bytes were unavailable" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing incomplete reason: %v", m.Warnings)
			}
		})
	}
}

func TestExportMissingOrTamperedEvidenceIsIncomplete(t *testing.T) {
	cases := []struct {
		name   string
		damage func(t *testing.T, dir string, artifact core.EvidenceArtifact)
	}{
		{
			name: "missing blob",
			damage: func(t *testing.T, dir string, artifact core.EvidenceArtifact) {
				if err := os.Remove(filepath.Join(dir, "evidence", "blobs", artifact.SHA256)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hash changed",
			damage: func(t *testing.T, dir string, artifact core.EvidenceArtifact) {
				writeFileR(t, dir, "evidence/blobs/"+artifact.SHA256, "tampered evidence bytes\n")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, repo, artifact := evidenceFixture(t)
			tc.damage(t, dir, artifact)
			pkg := exportToPackage(t, dir, repo, true)
			m := verifySealedPackage(t, pkg)
			if m.RecoveryStatus != handoff.StatusIncomplete {
				t.Fatalf("status = %q", m.RecoveryStatus)
			}
			if m.EvidenceBytesIncluded != 0 {
				t.Fatalf("evidence bytes included = %d", m.EvidenceBytesIncluded)
			}
			joined := strings.Join(m.Warnings, "|")
			if !strings.Contains(joined, "projection has pending or conflicting events") {
				t.Fatalf("warnings = %v", m.Warnings)
			}
			if packageHasPath(m, "payload/evidence/"+artifact.SHA256) {
				t.Fatal("unverifiable evidence bytes were exported")
			}
		})
	}
}

func TestExportProjectionIncomplete(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-pending", "dev-pending")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
	repo := initGitRepo(t)
	pendingBatchR(t, dir)
	pkg := exportToPackage(t, dir, repo, false)
	m := verifySealedPackage(t, pkg)
	if m.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("status = %q", m.RecoveryStatus)
	}
	found := false
	for _, w := range m.Warnings {
		if w == "projection has pending or conflicting events" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", m.Warnings)
	}
}

func TestExportQuarantinedBatch(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-quarantine", "dev-quarantine")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
	writeFileR(t, dir, "events/bad.json", "{not json")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	m := verifySealedPackage(t, pkg)
	if m.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("status = %q", m.RecoveryStatus)
	}
	found := false
	for _, w := range m.Warnings {
		if w == "some event batches are quarantined; recovery is incomplete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", m.Warnings)
	}
}

func TestExportRefusesBadTargets(t *testing.T) {
	dir, repo, _ := evidenceFixture(t)

	t.Run("existing directory", func(t *testing.T) {
		out := t.TempDir()
		if _, _, err := Export(dir, repo, out, false); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
			t.Fatalf("existing target: %v", err)
		}
	})
	t.Run("existing file", func(t *testing.T) {
		out := writeFileR(t, t.TempDir(), "file", "x")
		if _, _, err := Export(dir, repo, out, false); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
			t.Fatalf("existing file target: %v", err)
		}
	})
	t.Run("missing parent", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "missing", "pkg")
		if _, _, err := Export(dir, repo, out, false); err == nil || !strings.Contains(err.Error(), "parent directory does not exist") {
			t.Fatalf("missing parent: %v", err)
		}
	})
}

func TestExportErrorPaths(t *testing.T) {
	t.Run("uninitialized workspace", func(t *testing.T) {
		if _, _, err := Export(t.TempDir(), initGitRepo(t), filepath.Join(t.TempDir(), "pkg"), false); err == nil {
			t.Fatal("Export accepted an uninitialized workspace")
		}
	})

	t.Run("identity device id wrong type", func(t *testing.T) {
		dir, store := newWorkspaceR(t, "ws-bad", "dev-bad")
		_ = store
		// core.Open only needs workspace_id and event_log_version; workspaceIdentity
		// also decodes device_id, so a numeric device_id fails there and not earlier.
		writeFileR(t, dir, "manifest.json", `{"event_log_version":2,"workspace_id":"ws-bad","device_id":123}`)
		_, _, err := Export(dir, initGitRepo(t), filepath.Join(t.TempDir(), "pkg"), false)
		if err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
			t.Fatalf("workspace identity error: %v", err)
		}
	})

	t.Run("code state failure", func(t *testing.T) {
		dir, _ := newWorkspaceR(t, "ws-norepo", "dev-norepo")
		if _, _, err := Export(dir, t.TempDir(), filepath.Join(t.TempDir(), "pkg"), false); err == nil {
			t.Fatal("Export accepted a non-repository")
		}
	})

	t.Run("event batch directory contains symlink", func(t *testing.T) {
		dir, store := newWorkspaceR(t, "ws-symlink-events", "dev")
		gc := appendClaimR(t, store, "g1", "lg")
		appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
		target := writeFileR(t, t.TempDir(), "outside.json", "{}\n")
		if err := os.Symlink(target, filepath.Join(dir, "events", "link.json")); err != nil {
			t.Fatal(err)
		}
		_, _, err := Export(dir, initGitRepo(t), filepath.Join(t.TempDir(), "pkg"), false)
		if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("symlink batch: %v", err)
		}
	})

	t.Run("event batch unreadable", func(t *testing.T) {
		dir, store := newWorkspaceR(t, "ws-unreadable-events", "dev")
		gc := appendClaimR(t, store, "g1", "lg")
		appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
		batches, err := store.EventBatchPaths()
		if err != nil || len(batches) == 0 {
			t.Fatalf("batches: %v %v", batches, err)
		}
		chmodR(t, batches[0], 0)
		if _, _, err := Export(dir, initGitRepo(t), filepath.Join(t.TempDir(), "pkg"), false); err == nil {
			t.Fatal("Export read an unreadable event batch")
		}
	})

	t.Run("workspace manifest carries a secret", func(t *testing.T) {
		dir, _ := newWorkspaceR(t, "ws-secret-manifest", "dev")
		writeFileR(t, dir, "manifest.json",
			`{"schema_version":1,"event_log_version":2,"workspace_id":"ws-secret-manifest","device_id":"password = hunter2hunter2"}`)
		_, _, err := Export(dir, initGitRepo(t), filepath.Join(t.TempDir(), "pkg"), false)
		if err == nil || !strings.Contains(err.Error(), "payload/workspace/manifest.json") {
			t.Fatalf("secret workspace manifest: %v", err)
		}
	})

	t.Run("event batch content carries a secret", func(t *testing.T) {
		dir, _ := newWorkspaceR(t, "ws-secret-event", "dev")
		writeFileR(t, dir, "events/evil.json", `{"note":"password = hunter2hunter2"}`)
		_, _, err := Export(dir, initGitRepo(t), filepath.Join(t.TempDir(), "pkg"), false)
		if err == nil || !strings.Contains(err.Error(), "payload/events/evil.json") {
			t.Fatalf("secret event batch: %v", err)
		}
	})

	t.Run("untracked content carries a secret", func(t *testing.T) {
		repo := newRepoR(t)
		writeFileR(t, repo, "notes.txt", "password = hunter2hunter2\n")
		dir, _ := newWorkspaceR(t, "ws-secret-untracked", "dev")
		_, _, err := Export(dir, repo, filepath.Join(t.TempDir(), "pkg"), true)
		if err == nil || !strings.Contains(err.Error(), "payload/untracked/notes.txt") {
			t.Fatalf("secret untracked content: %v", err)
		}
	})

	t.Run("untracked path carries a secret", func(t *testing.T) {
		repo := newRepoR(t)
		writeFileR(t, repo, "password=hunter2hunter2.txt", "clean\n")
		dir, _ := newWorkspaceR(t, "ws-secret-path", "dev")
		_, _, err := Export(dir, repo, filepath.Join(t.TempDir(), "pkg"), false)
		if err == nil || !strings.Contains(err.Error(), "export manifest rejected") {
			t.Fatalf("secret untracked path: %v", err)
		}
	})

	t.Run("staged diff carries a secret", func(t *testing.T) {
		repo := newRepoR(t)
		writeFileR(t, repo, "tracked.txt", "password = hunter2hunter2\n")
		runGitR(t, repo, "add", "tracked.txt")
		dir, _ := newWorkspaceR(t, "ws-secret-staged", "dev")
		_, _, err := Export(dir, repo, filepath.Join(t.TempDir(), "pkg"), true)
		if err == nil || !strings.Contains(err.Error(), "payload/git/staged.diff") {
			t.Fatalf("secret staged diff: %v", err)
		}
	})

	t.Run("unstaged diff carries a secret", func(t *testing.T) {
		repo := newRepoR(t)
		writeFileR(t, repo, "tracked.txt", "password = hunter2hunter2\n")
		dir, _ := newWorkspaceR(t, "ws-secret-unstaged", "dev")
		_, _, err := Export(dir, repo, filepath.Join(t.TempDir(), "pkg"), true)
		if err == nil || !strings.Contains(err.Error(), "payload/git/unstaged.diff") {
			t.Fatalf("secret unstaged diff: %v", err)
		}
	})
}

func TestExportFilesAreSorted(t *testing.T) {
	dir, repo, _ := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, true)
	m := readManifestR(t, pkg)
	var paths []string
	for _, f := range m.Files {
		paths = append(paths, f.Path)
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("manifest files are not sorted: %v", paths)
	}
}

func TestExportDeduplicatesSharedEvidenceBlobs(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-shared-evidence", "dev")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Ship", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
	tc := appendClaimR(t, store, "t1", "lt")
	created := appendNodeR(t, store, "t1", string(core.NodeTask), "Carry", "g1", "lt", core.NodePayload{}, tc.EventID)
	// Two evidence ids that reference the same content-addressed blob.
	artifact := appendEvidenceR(t, store, "t1", "ev1", "shared evidence bytes\n", created.EventID)
	src := writeFileR(t, t.TempDir(), "artifact.txt", "shared evidence bytes\n")
	second, err := store.PutEvidenceFile(src, "", []string{"ev2", "t1", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != artifact.SHA256 {
		t.Fatalf("expected identical digests, got %q and %q", second.SHA256, artifact.SHA256)
	}
	payload, err := json.Marshal(core.EvidencePayload{
		EvidenceID: "ev2", NodeID: "t1", Kind: "test", SHA256: second.SHA256,
		Size: second.Size, ArtifactSchema: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(core.Event{Type: core.EvEvidenceAdded, CausalParents: []string{created.EventID}, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, true)
	m := verifySealedPackage(t, pkg)
	if m.EvidenceBytesIncluded != 1 {
		t.Fatalf("evidence_bytes_included = %d, want unique blobs 1", m.EvidenceBytesIncluded)
	}
	if got := countEntries(m.Files, "payload/evidence/"); got != 1 {
		t.Fatalf("evidence package files = %d, want 1", got)
	}
	if m.RecoveryStatus != handoff.StatusComplete {
		t.Fatalf("status = %q warnings=%v", m.RecoveryStatus, m.Warnings)
	}
}

// testTextconvRepo configures a git textconv driver for tracked.txt. A counter
// script makes every diff invocation produce different bytes (hash-changed
// branch); "false" makes git diff fail outright (unavailable branch).
func testTextconvRepo(t *testing.T, bump bool) string {
	t.Helper()
	repo := newRepoR(t)
	var cmd string
	if bump {
		counter := filepath.Join(t.TempDir(), "counter")
		script := filepath.Join(t.TempDir(), "bump.sh")
		body := "#!/bin/sh\n" +
			"c=" + counter + "\n" +
			"n=$(cat \"$c\" 2>/dev/null || echo 0)\n" +
			"n=$((n+1))\n" +
			"echo \"$n\" > \"$c\"\n" +
			"echo revision-$n\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd = script
		runGitR(t, repo, "config", "diff.bump.textconv", cmd)
		writeFileR(t, repo, ".gitattributes", "tracked.txt diff=bump\n")
	} else {
		runGitR(t, repo, "config", "diff.fail.textconv", "false")
		writeFileR(t, repo, ".gitattributes", "tracked.txt diff=fail\n")
	}
	writeFileR(t, repo, "tracked.txt", "staged version\n")
	runGitR(t, repo, "add", "tracked.txt")
	writeFileR(t, repo, "tracked.txt", "unstaged version\n")
	return repo
}

func TestExportDiffHashChangedIsExcluded(t *testing.T) {
	repo := testTextconvRepo(t, true)
	dir, _ := newWorkspaceR(t, "ws-textconv", "dev")
	pkg := exportToPackage(t, dir, repo, true)
	m := verifySealedPackage(t, pkg)
	if m.StagedDiffIncluded || m.UnstagedDiffIncluded {
		t.Fatalf("hash-changed diffs were included: %+v", m)
	}
	if packageHasPath(m, "payload/git/staged.diff") || packageHasPath(m, "payload/git/unstaged.diff") {
		t.Fatalf("hash-changed diff bytes exported: %+v", m.Files)
	}
	joined := strings.Join(m.Warnings, "|")
	if !strings.Contains(joined, "staged diff hash changed; bytes excluded") {
		t.Fatalf("warnings = %v", m.Warnings)
	}
	if !strings.Contains(joined, "unstaged diff hash changed; bytes excluded") {
		t.Fatalf("warnings = %v", m.Warnings)
	}
}

func TestExportUnavailableDiffBytesWarn(t *testing.T) {
	repo := testTextconvRepo(t, false)
	dir, _ := newWorkspaceR(t, "ws-textconv-fail", "dev")
	pkg := exportToPackage(t, dir, repo, true)
	m := verifySealedPackage(t, pkg)
	if m.StagedDiffIncluded || m.UnstagedDiffIncluded {
		t.Fatalf("unavailable diffs were included: %+v", m)
	}
	joined := strings.Join(m.Warnings, "|")
	if !strings.Contains(joined, "staged diff bytes unavailable") {
		t.Fatalf("warnings = %v", m.Warnings)
	}
	if !strings.Contains(joined, "unstaged diff bytes unavailable") {
		t.Fatalf("warnings = %v", m.Warnings)
	}
}

func TestExportOversizeWorkspaceManifestFails(t *testing.T) {
	dir, _ := newWorkspaceR(t, "ws-oversize-manifest", "dev")
	pad := strings.Repeat("a", MaxFileBytes)
	manifest := `{"schema_version":1,"event_log_version":2,"workspace_id":"ws-oversize-manifest","device_id":"dev","pad":"` + pad + `"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "pkg")
	_, _, err := Export(dir, initGitRepo(t), out, false)
	if err == nil {
		t.Fatal("oversize workspace manifest was exported")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed export left a package directory")
	}
}

func TestExportTargetPathErrors(t *testing.T) {
	dir, repo, _ := evidenceFixture(t)

	t.Run("path under a regular file", func(t *testing.T) {
		file := writeFileR(t, t.TempDir(), "file", "x")
		out := filepath.Join(file, "pkg")
		if _, _, err := Export(dir, repo, out, false); err == nil {
			t.Fatal("export accepted a path below a regular file")
		}
	})

	t.Run("shared agent config target", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		out := filepath.Join(home, ".claude")
		if _, _, err := Export(dir, repo, out, false); err == nil || !strings.Contains(err.Error(), "shared agent config") {
			t.Fatalf("shared agent config target: %v", err)
		}
	})

	t.Run("unwritable parent", func(t *testing.T) {
		parent := t.TempDir()
		chmodR(t, parent, 0o500)
		out := filepath.Join(parent, "pkg")
		if _, _, err := Export(dir, repo, out, false); err == nil {
			t.Fatal("export created staging in an unwritable parent")
		}
	})
}

func TestExportEvidenceWithUppercaseDigest(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-upper-evidence", "dev")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
	tc := appendClaimR(t, store, "t1", "lt")
	created := appendNodeR(t, store, "t1", string(core.NodeTask), "Task", "g1", "lt", core.NodePayload{}, tc.EventID)
	src := writeFileR(t, t.TempDir(), "artifact.txt", "uppercase digest evidence\n")
	artifact, err := store.PutEvidenceFile(src, "", []string{"ev1", "t1", "test"})
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(artifact.SHA256)
	payload, err := json.Marshal(core.EvidencePayload{
		EvidenceID: "ev1", NodeID: "t1", Kind: "test", SHA256: upper,
		Size: artifact.Size, ArtifactSchema: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Append(core.Event{Type: core.EvEvidenceAdded, CausalParents: []string{created.EventID}, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, true)
	m := verifySealedPackage(t, pkg)
	// Uppercase hex is normalized to lowercase at replay time, so the blob is
	// found on case-sensitive filesystems and exported (and verifiable) too.
	if m.EvidenceBytesIncluded != 1 {
		t.Fatalf("evidence bytes included = %d (warnings=%v)", m.EvidenceBytesIncluded, m.Warnings)
	}
	if m.RecoveryStatus != handoff.StatusComplete {
		t.Fatalf("status = %q (warnings=%v)", m.RecoveryStatus, m.Warnings)
	}
	for _, w := range m.Warnings {
		if w == "evidence hash changed: ev1" || strings.Contains(w, "unreadable") {
			t.Fatalf("unexpected warning for a normalized digest: %v", m.Warnings)
		}
	}
}
