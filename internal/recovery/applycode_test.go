package recovery

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/core"
)

func gitOutR(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func initEmptyRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.invalid"}, {"config", "user.name", "T"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

func codeFixture(t *testing.T) (dir, repo string) {
	t.Helper()
	repo = newRepoR(t) // committed tracked.txt
	dir = t.TempDir()
	if _, err := core.InitWorkspace(dir, "ws-code", "dev-code"); err != nil {
		t.Fatal(err)
	}
	return dir, repo
}

func TestApplyCodeRoundTripIntoFreshRepo(t *testing.T) {
	dir, repoA := codeFixture(t)
	pkg := filepath.Join(t.TempDir(), "pkg")
	m, _, err := ExportWithCode(dir, repoA, pkg, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m.CodeBundleIncluded || m.CodeBundleSHA256 == "" {
		t.Fatalf("export did not record the bundle: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(pkg, "payload", "git", "code.bundle")); err != nil {
		t.Fatal("bundle file missing from package")
	}

	repoB := initEmptyRepo(t)
	plan, err := ApplyCode(pkg, repoB, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.BundleIncluded || !plan.BundleVerified || plan.Applied || len(plan.Blockers) != 0 {
		t.Fatalf("plan wrong: %+v", plan)
	}
	if plan.PacketHead == "" || !strings.Contains(strings.Join(plan.Actions, "|"), "fetch") {
		t.Fatalf("plan actions incomplete: %+v", plan)
	}

	applied, err := ApplyCode(pkg, repoB, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.NewHead != applied.PacketHead {
		t.Fatalf("apply wrong: %+v", applied)
	}
	if got := gitOutR(t, repoB, "rev-parse", "HEAD"); got != applied.PacketHead {
		t.Fatalf("repo HEAD = %s, want %s", got, applied.PacketHead)
	}
	if b, err := os.ReadFile(filepath.Join(repoB, "tracked.txt")); err != nil || string(b) != "clean\n" {
		t.Fatalf("tracked file not restored: %q %v", b, err)
	}
}

func TestApplyCodeDirtyTargetNeedsForceAndBacksUp(t *testing.T) {
	dir, repoA := codeFixture(t)
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	repoB := initEmptyRepo(t)
	// Give repo B its own commit and a dirty file.
	if err := os.WriteFile(filepath.Join(repoB, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoB, "add", "local.txt")
	gitOutR(t, repoB, "commit", "-qm", "local")
	ownHead := gitOutR(t, repoB, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repoB, "local.txt"), []byte("dirty local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := ApplyCode(pkg, repoB, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) == 0 || !strings.Contains(strings.Join(plan.Blockers, " "), "dirty") {
		t.Fatalf("plan should block a dirty target: %+v", plan)
	}
	if _, err := ApplyCode(pkg, repoB, false, false); err == nil {
		t.Fatal("dirty target was applied without --force")
	}
	applied, err := ApplyCode(pkg, repoB, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || !applied.Stashed || applied.BackupRef == "" {
		t.Fatalf("forced apply did not back up: %+v", applied)
	}
	if got := gitOutR(t, repoB, "rev-parse", applied.BackupRef); got != ownHead {
		t.Fatalf("backup ref %s = %s, want %s", applied.BackupRef, got, ownHead)
	}
	if got := gitOutR(t, repoB, "rev-parse", "HEAD"); got != applied.PacketHead {
		t.Fatalf("HEAD = %s, want %s", got, applied.PacketHead)
	}
}

func TestApplyCodeRestoresUncommittedWorktree(t *testing.T) {
	dir, repoA := codeFixture(t)
	// staged change, unstaged change, untracked file
	if err := os.WriteFile(filepath.Join(repoA, "tracked.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoA, "add", "tracked.txt")
	if err := os.WriteFile(filepath.Join(repoA, "tracked.txt"), []byte("unstaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoA, "notes.txt"), []byte("untracked notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "pkg")
	m, _, err := ExportWithCode(dir, repoA, pkg, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m.StagedDiffIncluded || !m.UnstagedDiffIncluded || m.UntrackedBytesIncluded != 1 {
		t.Fatalf("dirty export incomplete: %+v", m)
	}
	repoB := initEmptyRepo(t)
	applied, err := ApplyCode(pkg, repoB, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || !applied.StagedDiffApplied || !applied.UnstagedDiffApplied || applied.UntrackedWritten != 1 {
		t.Fatalf("apply did not restore uncommitted state: %+v", applied)
	}
	if b, err := os.ReadFile(filepath.Join(repoB, "tracked.txt")); err != nil || string(b) != "unstaged\n" {
		t.Fatalf("unstaged content not restored: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(repoB, "notes.txt")); err != nil || string(b) != "untracked notes\n" {
		t.Fatalf("untracked content not restored: %q %v", b, err)
	}
	if out := gitOutR(t, repoB, "status", "--porcelain"); !strings.Contains(out, "notes.txt") {
		t.Fatalf("untracked file not visible to git: %s", out)
	}
}

func TestApplyCodeRejectsBadPackagesAndTargets(t *testing.T) {
	dir, repoA := codeFixture(t)
	// Package without a bundle.
	noBundle := filepath.Join(t.TempDir(), "nobundle")
	if _, _, err := ExportWithCode(dir, repoA, noBundle, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCode(noBundle, initEmptyRepo(t), true, false); err == nil || !strings.Contains(err.Error(), "no code bundle") {
		t.Fatalf("package without a bundle accepted: %v", err)
	}
	// Tampered bundle bytes.
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(pkg, "payload", "git", "code.bundle")
	b, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(bundle, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCode(pkg, initEmptyRepo(t), true, false); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered bundle accepted: %v", err)
	}
	// Target that is not a git work tree.
	plainPkg := filepath.Join(t.TempDir(), "pkg2")
	if _, _, err := ExportWithCode(dir, repoA, plainPkg, false, true); err != nil {
		t.Fatal(err)
	}
	notRepo := t.TempDir()
	if _, err := ApplyCode(plainPkg, notRepo, true, false); err == nil || !strings.Contains(err.Error(), "not a git working tree") {
		t.Fatalf("non-git target accepted: %v", err)
	}
}

func TestApplyCodeUntrackedConflictBlocks(t *testing.T) {
	dir, repoA := codeFixture(t)
	if err := os.WriteFile(filepath.Join(repoA, "notes.txt"), []byte("from source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, true, true); err != nil {
		t.Fatal(err)
	}
	repoB := initEmptyRepo(t)
	// The same untracked path exists in the target before the original commit.
	if err := os.WriteFile(filepath.Join(repoB, "notes.txt"), []byte("local copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := ApplyCode(pkg, repoB, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Blockers, " "), "already exists") {
		t.Fatalf("plan should block an existing untracked path: %+v", plan)
	}
	if _, err := ApplyCode(pkg, repoB, false, false); err == nil {
		t.Fatal("apply overwrote an existing untracked path")
	}
	if b, _ := os.ReadFile(filepath.Join(repoB, "notes.txt")); string(b) != "local copy\n" {
		t.Fatalf("local file was touched: %q", b)
	}
}

func TestApplyCodeDetachedHeadPacket(t *testing.T) {
	dir, repoA := codeFixture(t)
	head := gitOutR(t, repoA, "rev-parse", "HEAD")
	gitOutR(t, repoA, "checkout", "--detach", head)
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	repoB := initEmptyRepo(t)
	applied, err := ApplyCode(pkg, repoB, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.NewHead != head {
		t.Fatalf("detached packet not applied: %+v", applied)
	}
	if got := gitOutR(t, repoB, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
}

func TestApplyCodeRejectsDiffThatDoesNotApply(t *testing.T) {
	dir, repoA := codeFixture(t)
	if err := os.WriteFile(filepath.Join(repoA, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoA, "add", "tracked.txt")
	pkg := filepath.Join(t.TempDir(), "pkg")
	m, _, err := ExportWithCode(dir, repoA, pkg, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m.StagedDiffIncluded {
		t.Fatalf("expected a staged diff: %+v", m)
	}
	// Replace the staged diff with a syntactically valid but non-applying patch
	// and rebind the manifest hash: verification passes, application must fail.
	diffPath := filepath.Join(pkg, "payload", "git", "staged.diff")
	bogus := []byte("diff --git a/tracked.txt b/tracked.txt\n--- a/tracked.txt\n+++ b/tracked.txt\n@@ -1 +1 @@\n-not the file content\n+whatever\n")
	if err := os.WriteFile(diffPath, bogus, 0o600); err != nil {
		t.Fatal(err)
	}
	mb, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	for i := range man.Files {
		if man.Files[i].Path == "payload/git/staged.diff" {
			man.Files[i].SHA256 = hashBytes(bogus)
			man.Files[i].Size = int64(len(bogus))
		}
	}
	clone := man
	clone.ContentHash = ""
	body, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	man.ContentHash = hashBytes(body)
	sealed, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), append(sealed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCode(pkg, initEmptyRepo(t), false, false); err == nil {
		t.Fatal("diff that does not match the packet hash was applied")
	} else if !strings.Contains(err.Error(), "does not match the packet hash") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// doctorPackage replaces one payload file, rebinds its hash and re-seals the
// package so hash verification passes but content is malicious/broken.
func doctorPackage(t *testing.T, pkg, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(pkg, filepath.FromSlash(rel))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	mb, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	for i := range man.Files {
		if man.Files[i].Path == rel {
			man.Files[i].SHA256 = hashBytes(data)
			man.Files[i].Size = int64(len(data))
		}
	}
	clone := man
	clone.ContentHash = ""
	body, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	man.ContentHash = hashBytes(body)
	sealed, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), append(sealed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCodeRejectsCorruptedBundle(t *testing.T) {
	dir, repoA := codeFixture(t)
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(pkg, "payload", "git", "code.bundle")
	b, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt but keep the same length, then rebind the package hash so only
	// `git bundle verify` can catch it.
	b[len(b)/3] ^= 0xff
	doctorPackage(t, pkg, "payload/git/code.bundle", b)
	// Depending on where the bytes are damaged, either `git bundle verify` or the
	// fetch itself must fail closed.
	if _, err := ApplyCode(pkg, initEmptyRepo(t), false, false); err == nil {
		t.Fatal("corrupted bundle was accepted")
	} else if !strings.Contains(err.Error(), "failed verification") && !strings.Contains(err.Error(), "cannot fetch") && !strings.Contains(err.Error(), "inflate") {
		t.Fatalf("corrupted bundle failure was not detected: %v", err)
	}
}

func TestApplyCodeRejectsSecretBearingDiff(t *testing.T) {
	dir, repoA := codeFixture(t)
	if err := os.WriteFile(filepath.Join(repoA, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoA, "add", "tracked.txt")
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, true, true); err != nil {
		t.Fatal(err)
	}
	bogus := []byte("diff --git a/tracked.txt b/tracked.txt\n--- a/tracked.txt\n+++ b/tracked.txt\n@@ -1 +1 @@\n-clean\n+AKIAIOSFODNN7EXAMPLE\n")
	doctorPackage(t, pkg, "payload/git/staged.diff", bogus)
	if _, err := ApplyCode(pkg, initEmptyRepo(t), false, false); err == nil || !strings.Contains(strings.ToLower(err.Error()), "secret") {
		t.Fatalf("secret-bearing diff was applied: %v", err)
	}
}

// resealManifest rewrites the package manifest after mutating it, recomputing
// the content hash so hash verification still passes.
func resealManifest(t *testing.T, pkg string, mutate func(*Manifest)) {
	t.Helper()
	mb, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	mutate(&m)
	clone := m
	clone.ContentHash = ""
	body, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	m.ContentHash = hashBytes(body)
	sealed, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), append(sealed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCodeRejectsUnsafeHandoffIDAndBranch(t *testing.T) {
	dir, repoA := codeFixture(t)
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	resealManifest(t, pkg, func(m *Manifest) {
		m.Packet.HandoffID = "../evil"
	})
	if _, err := ApplyCode(pkg, initEmptyRepo(t), true, false); err == nil || !strings.Contains(err.Error(), "unsafe handoff id") {
		t.Fatalf("unsafe handoff id accepted: %v", err)
	}

	pkg2 := filepath.Join(t.TempDir(), "pkg2")
	if _, _, err := ExportWithCode(dir, repoA, pkg2, false, true); err != nil {
		t.Fatal(err)
	}
	resealManifest(t, pkg2, func(m *Manifest) {
		m.Packet.CodeState.Branch = "--upload-pack=evil"
	})
	if _, err := ApplyCode(pkg2, initEmptyRepo(t), true, false); err == nil || !strings.Contains(err.Error(), "unsafe branch name") {
		t.Fatalf("option-like branch name accepted: %v", err)
	}
}

func TestApplyCodeRollsBackPartialApplication(t *testing.T) {
	dir, repoA := codeFixture(t)
	// One staged change and one unstaged change.
	if err := os.WriteFile(filepath.Join(repoA, "tracked.txt"), []byte("staged ver\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoA, "add", "tracked.txt")
	if err := os.WriteFile(filepath.Join(repoA, "tracked.txt"), []byte("unstaged ver\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "pkg")
	m, _, err := ExportWithCode(dir, repoA, pkg, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m.StagedDiffIncluded || !m.UnstagedDiffIncluded {
		t.Fatalf("expected both diffs: %+v", m)
	}
	// Make the unstaged diff non-applying but matching the *packet* hash, so the
	// pre-flight hash check passes and application itself fails.
	bogus := []byte("diff --git a/tracked.txt b/tracked.txt\n--- a/tracked.txt\n+++ b/tracked.txt\n@@ -1 +1 @@\n-not the content\n+x\n")
	if err := os.WriteFile(filepath.Join(pkg, "payload", "git", "unstaged.diff"), bogus, 0o600); err != nil {
		t.Fatal(err)
	}
	resealManifest(t, pkg, func(m *Manifest) {
		m.Packet.CodeState.UnstagedDiffSHA256 = hashBytes(bogus)
		for i := range m.Files {
			if m.Files[i].Path == "payload/git/unstaged.diff" {
				m.Files[i].SHA256 = hashBytes(bogus)
				m.Files[i].Size = int64(len(bogus))
			}
		}
	})

	repoB := initEmptyRepo(t)
	if err := os.WriteFile(filepath.Join(repoB, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoB, "add", "local.txt")
	gitOutR(t, repoB, "commit", "-qm", "local")
	before := gitOutR(t, repoB, "rev-parse", "HEAD")

	if _, err := ApplyCode(pkg, repoB, false, false); err == nil {
		t.Fatal("non-applying unstaged diff was accepted")
	} else if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("failure did not report rollback: %v", err)
	}
	if got := gitOutR(t, repoB, "rev-parse", "HEAD"); got != before {
		t.Fatalf("worktree not rolled back: HEAD=%s want %s", got, before)
	}
	if b, err := os.ReadFile(filepath.Join(repoB, "local.txt")); err != nil || string(b) != "local\n" {
		t.Fatalf("local file lost after rollback: %q %v", b, err)
	}
}
