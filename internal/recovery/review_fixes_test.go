package recovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/handoff"
)

// doctorAddPayload writes a payload file into a verified package and rebinds the
// manifest entry, optionally extending the packet's code state (for crafting
// adversarial but hash-consistent packages).
func doctorAddPayload(t *testing.T, pkg, rel string, data []byte, kind string, mutatePacket func(*Manifest)) {
	t.Helper()
	path := filepath.Join(pkg, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	resealManifest(t, pkg, func(m *Manifest) {
		m.Files = append(m.Files, FileEntry{Path: rel, SHA256: hashBytes(data), Size: int64(len(data)), Kind: kind})
		if mutatePacket != nil {
			mutatePacket(m)
		}
	})
}

// TestApplyCodeRefusesSymlinkedParentEscape covers review finding F5.
func TestApplyCodeRefusesSymlinkedParentEscape(t *testing.T) {
	outside := t.TempDir()
	dir, repoA := codeFixture(t)
	// Commit a symlink that points outside the repository.
	if err := os.Symlink(outside, filepath.Join(repoA, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitOutR(t, repoA, "add", "link")
	gitOutR(t, repoA, "commit", "-qm", "symlink")
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, true, true); err != nil {
		t.Fatal(err)
	}
	payload := []byte("escaped\n")
	doctorAddPayload(t, pkg, "payload/untracked/link/pwn.txt", payload, "untracked", func(m *Manifest) {
		m.Packet.CodeState.Untracked = append(m.Packet.CodeState.Untracked,
			handoffFileRef("link/pwn.txt", hashBytes(payload), int64(len(payload))))
	})

	repoB := initEmptyRepo(t)
	plan, err := ApplyCode(pkg, repoB, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		// Plan phase may not inspect symlinks yet; apply must still refuse.
		t.Logf("plan blockers: %v", plan.Blockers)
	}
	if _, err := ApplyCode(pkg, repoB, false, false); err == nil {
		t.Fatal("apply-code wrote through a symlinked directory")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); !os.IsNotExist(err) {
		t.Fatal("apply-code escaped the target repository")
	}
}

// TestApplyCodeBlocksIgnoredFileOverwrite covers review finding F6.
func TestApplyCodeBlocksIgnoredFileOverwrite(t *testing.T) {
	dir, repoA := codeFixture(t)
	// repoA commits the file that repoB will ignore locally.
	if err := os.WriteFile(filepath.Join(repoA, "secret.env"), []byte("DB=source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoA, "add", "secret.env")
	gitOutR(t, repoA, "commit", "-qm", "env")
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}

	repoB := initEmptyRepo(t)
	if err := os.WriteFile(filepath.Join(repoB, ".gitignore"), []byte("secret.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoB, "add", ".gitignore")
	gitOutR(t, repoB, "commit", "-qm", "ignore")
	if err := os.WriteFile(filepath.Join(repoB, "secret.env"), []byte("local-only-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := gitOutR(t, repoB, "status", "--porcelain"); out != "" {
		t.Fatalf("ignored file leaked into status (test assumption broken): %q", out)
	}

	if _, err := ApplyCode(pkg, repoB, false, false); err == nil {
		t.Fatal("apply-code silently overwrote an ignored local file")
	} else if !strings.Contains(err.Error(), "ignored local files") {
		t.Fatalf("unexpected error: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repoB, "secret.env")); string(b) != "local-only-secret\n" {
		t.Fatalf("ignored local file changed: %q", b)
	}
	// --force stashes ignored files with --all before checking out.
	applied, err := ApplyCode(pkg, repoB, false, true)
	if err != nil {
		t.Fatalf("forced apply: %v", err)
	}
	if !applied.Stashed || len(applied.Warnings) == 0 {
		t.Fatalf("forced apply did not report the stash: %+v", applied)
	}
	if b, _ := os.ReadFile(filepath.Join(repoB, "secret.env")); string(b) != "DB=source\n" {
		t.Fatalf("source file not restored: %q", b)
	}
	stash, _ := exec.Command("git", "-C", repoB, "stash", "list").Output()
	if !strings.Contains(string(stash), "anchor pre-apply backup") {
		t.Fatalf("ignored local file not recoverable from the stash: %s", stash)
	}
}

// TestApplyCodeRefusesIncompleteCodePackage covers review finding F7.
func TestApplyCodeRefusesIncompleteCodePackage(t *testing.T) {
	dir, repoA := codeFixture(t)
	// An untracked file above the capture cap is Skipped, so the package cannot
	// reproduce the dirty state.
	big := make([]byte, 11<<20)
	if err := os.WriteFile(filepath.Join(repoA, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "pkg")
	m, _, err := ExportWithCode(dir, repoA, pkg, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.RecoveryStatus != "incomplete" {
		t.Fatalf("export should be incomplete with a skipped untracked file: %+v", m)
	}
	repoB := initEmptyRepo(t)
	plan, err := ApplyCode(pkg, repoB, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) == 0 || !strings.Contains(strings.Join(plan.Blockers, " "), "complete code bytes") {
		t.Fatalf("plan should refuse an incomplete code package: %+v", plan)
	}
	if _, err := ApplyCode(pkg, repoB, false, false); err == nil {
		t.Fatal("apply-code reported success for an incomplete code package")
	}
}

// TestApplyCodeDoesNotResetUnrelatedBranch covers review finding F16.
func TestApplyCodeDoesNotResetUnrelatedBranch(t *testing.T) {
	dir, repoA := codeFixture(t)
	branch := gitOutR(t, repoA, "symbolic-ref", "--short", "HEAD")
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	repoB := initEmptyRepo(t)
	gitOutR(t, repoB, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(repoB, "other.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoB, "add", "other.txt")
	gitOutR(t, repoB, "commit", "-qm", "unrelated")
	ownTip := gitOutR(t, repoB, "rev-parse", "HEAD")

	applied, err := ApplyCode(pkg, repoB, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied {
		t.Fatalf("apply failed: %+v", applied)
	}
	if got := gitOutR(t, repoB, "rev-parse", "refs/heads/"+branch); got != ownTip {
		t.Fatalf("unrelated local branch was reset: %s != %s", got, ownTip)
	}
	if got := gitOutR(t, repoB, "rev-parse", "HEAD"); got != applied.PacketHead {
		t.Fatalf("HEAD = %s, want %s", got, applied.PacketHead)
	}
}

// TestBundleSecretScanAsymmetry covers review finding F8: an export with a
// literal secret-shaped byte sequence in the bundle must still be importable.
func TestBundleSecretScanAsymmetry(t *testing.T) {
	dir, repoA := codeFixture(t)
	if err := os.WriteFile(filepath.Join(repoA, "fixture.txt"), []byte("AKIAIOSFODNN7EXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutR(t, repoA, "add", "fixture.txt")
	gitOutR(t, repoA, "commit", "-qm", "secret-shaped fixture")
	gitOutR(t, repoA, "config", "pack.compression", "0")
	pkg := filepath.Join(t.TempDir(), "pkg")
	if _, _, err := ExportWithCode(dir, repoA, pkg, false, true); err != nil {
		t.Fatal(err)
	}
	// The bundle is not text-scanned, so import must accept its own package.
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Import(pkg, target, false); err != nil {
		t.Fatalf("import rejected the exported bundle: %v", err)
	}
	// And apply-code restores the commit.
	if _, err := ApplyCode(pkg, initEmptyRepo(t), false, false); err != nil {
		t.Fatalf("apply-code rejected the exported bundle: %v", err)
	}
}

// helpers used above
func handoffFileRef(path, sha string, size int64) handoff.FileRef {
	return handoff.FileRef{Path: path, SHA256: sha, Size: size}
}
