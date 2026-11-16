package recovery

// Extra coverage tests for recovery: Import rebuild, atomicity, report contract.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

// setPackageFile writes rel (overwriting) and updates the manifest entry and
// content hash.
func setPackageFile(t *testing.T, pkg, rel string, data []byte, m Manifest) Manifest {
	t.Helper()
	dst := filepath.Join(pkg, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := FileEntry{Path: rel, SHA256: hashBytes(data), Size: int64(len(data)), Kind: "fixture"}
	found := false
	for i := range m.Files {
		if m.Files[i].Path == rel {
			m.Files[i] = entry
			found = true
		}
	}
	if !found {
		m.Files = append(m.Files, entry)
	}
	writeManifest(t, pkg, m)
	return m
}

func readReportFile(t *testing.T, target string) Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(target, "anchor-import.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func packageFilePaths(m Manifest) []string {
	var out []string
	for _, f := range m.Files {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

func stagingLeftovers(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".anchor-import-") || strings.HasPrefix(e.Name(), ".anchor-export-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestImportRebuildsWorkspaceComplete(t *testing.T) {
	dir, repo, artifact := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, true)
	outer := readManifestR(t, pkg)
	parent := t.TempDir()
	target := filepath.Join(parent, "restored")

	dry, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Writes != 0 || dry.FilesWritten != 0 {
		t.Fatalf("dry run wrote: %+v", dry)
	}
	if !dry.WorkspaceRebuilt || dry.EventBatches != outer.EventBatches || dry.RecoveryStatus != handoff.StatusComplete {
		t.Fatalf("dry run prediction = %+v", dry)
	}
	if dry.FilesVerified != len(outer.Files) || dry.BytesVerified <= 0 || !dry.DoesNotExecute ||
		dry.CodeApply != "disabled" || dry.Upload || dry.AutoInstall {
		t.Fatalf("dry run flags = %+v", dry)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry run created the target: %v", err)
	}

	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.DryRun || r.RecoveryStatus != handoff.StatusComplete || !r.WorkspaceRebuilt {
		t.Fatalf("import report = %+v", r)
	}
	if r.WorkspaceID != "ws-evidence" || r.DeviceID != "dev-evidence" {
		t.Fatalf("import identity = %q/%q", r.WorkspaceID, r.DeviceID)
	}
	if r.EventBatches != outer.EventBatches || r.EventsApplied == 0 {
		t.Fatalf("event counts = %+v", r)
	}
	if r.EvidenceVerified != 1 || r.ProjectionStatus != "complete" {
		t.Fatalf("verification counts = %+v", r)
	}
	wantFiles := len(outer.Files)
	if r.HandoffRestored {
		wantFiles++
	}
	if r.FilesWritten != wantFiles {
		t.Fatalf("files written = %d, want %d (payload files plus the restored handoff packet)", r.FilesWritten, wantFiles)
	}
	if r.Writes != r.FilesWritten+1 {
		t.Fatalf("writes = %d, want files_written+1 = %d", r.Writes, r.FilesWritten+1)
	}
	if !r.DoesNotExecute || r.CodeApply != "disabled" || r.AutoInstall || r.Upload {
		t.Fatalf("import safety flags = %+v", r)
	}
	leftovers := stagingLeftovers(t, parent)
	if len(leftovers) != 0 {
		t.Fatalf("staging leftovers: %v", leftovers)
	}

	// Every payload file is restored byte-for-byte.
	for _, rel := range packageFilePaths(outer) {
		want, err := os.ReadFile(filepath.Join(pkg, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		var gotPath string
		switch {
		case rel == "payload/workspace/manifest.json":
			gotPath = "manifest.json"
		case strings.HasPrefix(rel, "payload/events/"):
			gotPath = filepath.Join("events", strings.TrimPrefix(rel, "payload/events/"))
		case strings.HasPrefix(rel, "payload/evidence/"):
			gotPath = filepath.Join("evidence", "blobs", strings.TrimPrefix(rel, "payload/evidence/"))
		case strings.HasPrefix(rel, "payload/"):
			gotPath = filepath.Join("local", "imported", strings.TrimPrefix(rel, "payload/"))
		default:
			continue
		}
		got, err := os.ReadFile(filepath.Join(target, gotPath))
		if err != nil || string(got) != string(want) {
			t.Fatalf("restored %q -> %q mismatch: %v", rel, gotPath, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "evidence", "blobs", artifact.SHA256)); err != nil {
		t.Fatalf("evidence blob not restored: %v", err)
	}

	// The rebuilt workspace replays cleanly.
	store, err := core.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	if replay.RecoveryStatus != "complete" || len(replay.Tree.Evidence) != 1 {
		t.Fatalf("rebuilt replay = %+v", replay)
	}

	// The persisted report matches the returned report (including its own write).
	persisted := readReportFile(t, target)
	if persisted.Writes != r.FilesWritten+1 || persisted.FilesWritten != r.FilesWritten ||
		persisted.RecoveryStatus != r.RecoveryStatus || persisted.WorkspaceRebuilt != r.WorkspaceRebuilt ||
		persisted.EventBatches != r.EventBatches || persisted.EventsApplied != r.EventsApplied ||
		persisted.EvidenceVerified != r.EvidenceVerified || persisted.ProjectionStatus != r.ProjectionStatus {
		t.Fatalf("persisted report = %+v, returned = %+v", persisted, r)
	}
}

func TestImportZeroEventWorkspaceIsComplete(t *testing.T) {
	dir, _ := newWorkspaceR(t, "ws-empty", "dev-empty")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	outer := readManifestR(t, pkg)
	target := filepath.Join(t.TempDir(), "restored")

	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus != handoff.StatusComplete || !r.WorkspaceRebuilt {
		t.Fatalf("report = %+v", r)
	}
	// Export records the preparation, so the package carries exactly that event.
	if r.EventBatches != 1 || r.EventsApplied != 1 || r.EvidenceVerified != 0 || r.ProjectionStatus != "complete" {
		t.Fatalf("counts = %+v", r)
	}
	wantFiles := len(outer.Files)
	if r.HandoffRestored {
		wantFiles++
	}
	if r.FilesWritten != wantFiles || r.Writes != r.FilesWritten+1 {
		t.Fatalf("write counts = %+v", r)
	}
	persisted := readReportFile(t, target)
	if persisted.Writes != r.FilesWritten+1 || persisted.EventBatches != 1 {
		t.Fatalf("persisted report = %+v", persisted)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Fatalf("rebuilt manifest missing: %v", err)
	}
}

func TestImportMissingEvidenceIsIncomplete(t *testing.T) {
	dir, repo, _ := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, false)
	outer := readManifestR(t, pkg)
	target := filepath.Join(t.TempDir(), "restored")

	dry, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("dry run predicted %q", dry.RecoveryStatus)
	}
	found := false
	for _, w := range dry.Warnings {
		if w == "evidence bytes are missing; evidence cannot be verified after import" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dry run warnings = %v", dry.Warnings)
	}

	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus != handoff.StatusIncomplete || r.EvidenceVerified != 0 {
		t.Fatalf("report = %+v", r)
	}
	found = false
	for _, w := range r.Warnings {
		if w == "rebuilt projection is not complete (pending events or conflicts)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	wantFiles := len(outer.Files)
	if r.HandoffRestored {
		wantFiles++
	}
	if r.FilesWritten != wantFiles {
		t.Fatalf("files written = %d, want %d (payload files plus the restored handoff packet)", r.FilesWritten, wantFiles)
	}
}

func TestImportQuarantinedBatchIsIncomplete(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-quarantined-import", "dev")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
	writeFileR(t, dir, "events/bad.json", "{not json")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	outer := readManifestR(t, pkg)
	if len(outer.Packet.Quarantined) != 1 {
		t.Fatalf("packet quarantined = %+v", outer.Packet.Quarantined)
	}
	target := filepath.Join(t.TempDir(), "restored")

	dry, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("dry run predicted %q", dry.RecoveryStatus)
	}
	found := false
	for _, w := range dry.Warnings {
		if w == "package contains quarantined event batches; recovery is incomplete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dry run warnings = %v", dry.Warnings)
	}

	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("apply predicted %q", r.RecoveryStatus)
	}
	found = false
	for _, w := range r.Warnings {
		if w == "rebuilt workspace has quarantined event batches" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Fatalf("rebuilt manifest missing: %v", err)
	}
}

func TestImportDirtyExportedCodeIsIncomplete(t *testing.T) {
	repo := newRepoR(t)
	writeFileR(t, repo, "tracked.txt", "dirty code\n")
	dir, _ := newWorkspaceR(t, "ws-dirty-import", "dev-dirty")
	pkg := exportToPackage(t, dir, repo, false)
	target := filepath.Join(t.TempDir(), "restored")

	dry, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("dry run predicted %q", dry.RecoveryStatus)
	}
	found := false
	for _, w := range dry.Warnings {
		if w == "uncommitted code bytes are missing; the successor cannot reproduce the code state" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dry run warnings = %v", dry.Warnings)
	}

	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("apply predicted %q", r.RecoveryStatus)
	}
	joined := strings.Join(r.Warnings, "|")
	if !strings.Contains(joined, "uncommitted code bytes are missing") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

func TestImportIdentityMismatchLeavesNoTarget(t *testing.T) {
	dir, repo, _ := evidenceFixture(t)
	pkg := exportToPackage(t, dir, repo, true)
	m := readManifestR(t, pkg)
	raw, err := os.ReadFile(filepath.Join(pkg, "payload", "workspace", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ws map[string]any
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatal(err)
	}
	ws["workspace_id"] = "some-other-workspace"
	tampered, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	setPackageFile(t, pkg, "payload/workspace/manifest.json", tampered, m)

	parent := t.TempDir()
	target := filepath.Join(parent, "restored")
	_, err = Import(pkg, target, false)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("identity mismatch: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed import left a partial target: %v", err)
	}
	if leftovers := stagingLeftovers(t, parent); len(leftovers) != 0 {
		t.Fatalf("staging leftovers: %v", leftovers)
	}
}

func TestImportInvalidWorkspaceManifest(t *testing.T) {
	dir, _ := newWorkspaceR(t, "ws-bad-manifest", "dev")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	m := readManifestR(t, pkg)
	setPackageFile(t, pkg, "payload/workspace/manifest.json", []byte("{not json"), m)
	target := filepath.Join(t.TempDir(), "restored")
	_, err := Import(pkg, target, false)
	if err == nil || !strings.Contains(err.Error(), "rebuilt workspace manifest unreadable") {
		t.Fatalf("invalid workspace manifest: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("failed import left a partial target")
	}
}

func TestImportTargetValidation(t *testing.T) {
	dir, _ := newWorkspaceR(t, "ws-targets", "dev")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)

	t.Run("non-empty target", func(t *testing.T) {
		target := t.TempDir()
		writeFileR(t, target, "existing.txt", "x")
		if _, err := Import(pkg, target, true); err == nil || !strings.Contains(err.Error(), "must be empty") {
			t.Fatalf("non-empty target: %v", err)
		}
	})

	t.Run("target is a regular file", func(t *testing.T) {
		target := writeFileR(t, t.TempDir(), "file", "x")
		if _, err := Import(pkg, target, true); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("file target: %v", err)
		}
	})

	t.Run("target is a symlink", func(t *testing.T) {
		real := t.TempDir()
		target := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, target); err != nil {
			t.Fatal(err)
		}
		if _, err := Import(pkg, target, true); err == nil {
			t.Fatal("symlinked target accepted")
		}
	})

	t.Run("missing parent rejects even dry run", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "missing", "target")
		if _, err := Import(pkg, target, true); err == nil || !strings.Contains(err.Error(), "parent directory does not exist") {
			t.Fatalf("dry run missing parent: %v", err)
		}
		if _, err := Import(pkg, target, false); err == nil || !strings.Contains(err.Error(), "parent directory does not exist") {
			t.Fatalf("apply missing parent: %v", err)
		}
	})

	t.Run("invalid package is rejected before the target", func(t *testing.T) {
		if _, err := Import(t.TempDir(), filepath.Join(t.TempDir(), "target"), true); err == nil {
			t.Fatal("invalid package accepted")
		}
	})
}

func TestImportExistingEmptyTarget(t *testing.T) {
	dir, _ := newWorkspaceR(t, "ws-empty-target", "dev")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus != handoff.StatusComplete || !r.WorkspaceRebuilt {
		t.Fatalf("report = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Fatalf("manifest missing after rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "anchor-import.json")); err != nil {
		t.Fatalf("report missing after rename: %v", err)
	}
	if leftovers := stagingLeftovers(t, parent); len(leftovers) != 0 {
		t.Fatalf("staging leftovers: %v", leftovers)
	}
}

func TestImportNestedPackageEntryNamesRejected(t *testing.T) {
	t.Run("nested event name", func(t *testing.T) {
		dir, _ := newWorkspaceR(t, "ws-nested-event", "dev")
		repo := initGitRepo(t)
		pkg := exportToPackage(t, dir, repo, false)
		m := readManifestR(t, pkg)
		setPackageFile(t, pkg, "payload/events/nested/batch.json", []byte("{}\n"), m)
		target := filepath.Join(t.TempDir(), "restored")
		_, err := Import(pkg, target, false)
		if err == nil || !strings.Contains(err.Error(), "unsafe package entry name") {
			t.Fatalf("nested event name: %v", err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("failed import left a partial target")
		}
	})

	t.Run("nested evidence name", func(t *testing.T) {
		dir, _ := newWorkspaceR(t, "ws-nested-evidence", "dev")
		repo := initGitRepo(t)
		pkg := exportToPackage(t, dir, repo, false)
		m := readManifestR(t, pkg)
		setPackageFile(t, pkg, "payload/evidence/aa/bb", []byte("x"), m)
		target := filepath.Join(t.TempDir(), "restored")
		_, err := Import(pkg, target, false)
		if err == nil || !strings.Contains(err.Error(), "unsafe package entry name") {
			t.Fatalf("nested evidence name: %v", err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("failed import left a partial target")
		}
	})
}

func TestImportLegacyPackageReportContract(t *testing.T) {
	pkg := t.TempDir()
	data := []byte("legacy payload bytes\n")
	writeFileR(t, pkg, "payload/untracked/notes.txt", string(data))
	m := baseManifest()
	m.Files = []FileEntry{{Path: "payload/untracked/notes.txt", SHA256: hashBytes(data), Size: int64(len(data)), Kind: "untracked"}}
	writeManifest(t, pkg, m)
	target := filepath.Join(t.TempDir(), "restored")
	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkspaceRebuilt || r.RecoveryStatus != handoff.StatusIncomplete || r.FilesWritten != 1 {
		t.Fatalf("legacy report = %+v", r)
	}
	if r.Writes != r.FilesWritten+1 {
		t.Fatalf("legacy writes = %d, want %d", r.Writes, r.FilesWritten+1)
	}
	persisted := readReportFile(t, target)
	if persisted.Writes != r.Writes || persisted.FilesWritten != r.FilesWritten {
		t.Fatalf("persisted legacy report = %+v", persisted)
	}
}

func TestImportTargetPathErrors(t *testing.T) {
	dir, _ := newWorkspaceR(t, "ws-target-errors", "dev")
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)

	t.Run("shared agent config target", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		target := filepath.Join(home, ".claude")
		if _, err := Import(pkg, target, true); err == nil || !strings.Contains(err.Error(), "shared agent config") {
			t.Fatalf("shared agent config target: %v", err)
		}
	})

	t.Run("unreadable target directory", func(t *testing.T) {
		target := t.TempDir()
		chmodR(t, target, 0)
		if _, err := Import(pkg, target, true); err == nil {
			t.Fatal("unreadable target directory accepted")
		}
	})

	t.Run("unwritable parent for apply", func(t *testing.T) {
		parent := t.TempDir()
		chmodR(t, parent, 0o500)
		target := filepath.Join(parent, "restored")
		if _, err := Import(pkg, target, false); err == nil {
			t.Fatal("import created staging in an unwritable parent")
		}
	})
}

func TestImportProjectionIncomplete(t *testing.T) {
	dir, store := newWorkspaceR(t, "ws-pending-import", "dev")
	gc := appendClaimR(t, store, "g1", "lg")
	appendNodeR(t, store, "g1", string(core.NodeGoal), "Goal", "", "lg", core.NodePayload{NorthStar: "n"}, gc.EventID)
	pendingBatchR(t, dir)
	repo := initGitRepo(t)
	pkg := exportToPackage(t, dir, repo, false)
	target := filepath.Join(t.TempDir(), "restored")

	dry, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("dry run predicted %q", dry.RecoveryStatus)
	}
	found := false
	for _, w := range dry.Warnings {
		if w == "exported projection is not complete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dry run warnings = %v", dry.Warnings)
	}

	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus != handoff.StatusIncomplete {
		t.Fatalf("apply status = %q", r.RecoveryStatus)
	}
	found = false
	for _, w := range r.Warnings {
		if w == "rebuilt projection is not complete (pending events or conflicts)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("apply warnings = %v", r.Warnings)
	}
}

// requireCaseInsensitiveFS skips on filesystems where "A" and "a" are distinct
// paths; the colliding-path import errors below need a case-insensitive volume.
func requireCaseInsensitiveFS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Case"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "case")); err != nil {
		t.Skipf("filesystem is case-sensitive; colliding package paths are impossible")
	}
}

func TestImportCollidingPackagePathsFail(t *testing.T) {
	t.Run("legacy mirror", func(t *testing.T) {
		requireCaseInsensitiveFS(t)
		pkg := t.TempDir()
		data := []byte("legacy bytes\n")
		writeFileR(t, pkg, "payload/A", string(data))
		m := baseManifest()
		m.Files = []FileEntry{
			{Path: "payload/A", SHA256: hashBytes(data), Size: int64(len(data)), Kind: "untracked"},
			{Path: "payload/a", SHA256: hashBytes(data), Size: int64(len(data)), Kind: "untracked"},
		}
		writeManifest(t, pkg, m)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := Import(pkg, target, false); err == nil {
			t.Fatal("colliding legacy payload paths were imported")
		}
	})

	t.Run("event", func(t *testing.T) {
		requireCaseInsensitiveFS(t)
		dir, _ := newWorkspaceR(t, "ws-collide-event", "dev")
		repo := initGitRepo(t)
		pkg := exportToPackage(t, dir, repo, false)
		m := readManifestR(t, pkg)
		data := []byte("{}\n")
		m = setPackageFile(t, pkg, "payload/events/A.json", data, m)
		setPackageFile(t, pkg, "payload/events/a.json", data, m)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := Import(pkg, target, false); err == nil {
			t.Fatal("colliding event paths were imported")
		}
	})

	t.Run("evidence", func(t *testing.T) {
		requireCaseInsensitiveFS(t)
		dir, _ := newWorkspaceR(t, "ws-collide-evidence", "dev")
		repo := initGitRepo(t)
		pkg := exportToPackage(t, dir, repo, false)
		m := readManifestR(t, pkg)
		data := []byte("same blob\n")
		m = setPackageFile(t, pkg, "payload/evidence/ABC", data, m)
		setPackageFile(t, pkg, "payload/evidence/abc", data, m)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := Import(pkg, target, false); err == nil {
			t.Fatal("colliding evidence paths were imported")
		}
	})

	t.Run("artifact", func(t *testing.T) {
		requireCaseInsensitiveFS(t)
		dir, _ := newWorkspaceR(t, "ws-collide-artifact", "dev")
		repo := initGitRepo(t)
		pkg := exportToPackage(t, dir, repo, false)
		m := readManifestR(t, pkg)
		data := []byte("artifact bytes\n")
		m = setPackageFile(t, pkg, "payload/untracked/A.txt", data, m)
		setPackageFile(t, pkg, "payload/untracked/a.txt", data, m)
		target := filepath.Join(t.TempDir(), "restored")
		if _, err := Import(pkg, target, false); err == nil {
			t.Fatal("colliding artifact paths were imported")
		}
	})
}

func TestImportWritesImportedArtifacts(t *testing.T) {
	repo := newRepoR(t)
	dir, _, _ := evidenceFixtureWithRepo(t, repo)
	// Keep the worktree dirty so byte artifacts are transported.
	writeFileR(t, repo, "untracked.txt", "untracked bytes\n")
	writeFileR(t, repo, "tracked.txt", "staged version\n")
	runGitR(t, repo, "add", "tracked.txt")
	pkg := exportToPackage(t, dir, repo, true)
	outer := readManifestR(t, pkg)
	if countEntries(outer.Files, "payload/untracked/") == 0 && countEntries(outer.Files, "payload/git/") == 0 {
		t.Fatalf("fixture exported no artifacts: %+v", outer.Files)
	}
	target := filepath.Join(t.TempDir(), "restored")
	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := len(outer.Files)
	if r.HandoffRestored {
		wantFiles++
	}
	if r.FilesWritten != wantFiles {
		t.Fatalf("files written = %d, want %d (payload files plus the restored handoff packet)", r.FilesWritten, wantFiles)
	}
	if r.Writes != r.FilesWritten+1 {
		t.Fatalf("writes = %d", r.Writes)
	}
	got, err := os.ReadFile(filepath.Join(target, "local", "imported", "untracked", "untracked.txt"))
	if err != nil || string(got) != "untracked bytes\n" {
		t.Fatalf("imported artifact bytes = %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, "local", "imported", "git", "staged.diff")); err != nil {
		t.Fatalf("staged diff artifact missing: %v", err)
	}
}
