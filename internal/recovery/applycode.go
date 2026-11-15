package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

// CodeReport is the result of a read-only code-restore plan or of applying the
// package's code bytes to a repository.
type CodeReport struct {
	PackageDir          string   `json:"package_dir"`
	Repo                string   `json:"repo"`
	DryRun              bool     `json:"dry_run"`
	HandoffID           string   `json:"handoff_id"`
	PacketHead          string   `json:"packet_head"`
	PacketBranch        string   `json:"packet_branch"`
	RepoHead            string   `json:"repo_head,omitempty"`
	BundleIncluded      bool     `json:"bundle_included"`
	BundlePath          string   `json:"bundle_path,omitempty"`
	BundleSHA256        string   `json:"bundle_sha256,omitempty"`
	BundleVerified      bool     `json:"bundle_verified"`
	BackupRef           string   `json:"backup_ref,omitempty"`
	Stashed             bool     `json:"stashed,omitempty"`
	StashRef            string   `json:"stash_ref,omitempty"`
	Actions             []string `json:"actions,omitempty"`
	Blockers            []string `json:"blockers,omitempty"`
	Applied             bool     `json:"applied"`
	NewHead             string   `json:"new_head,omitempty"`
	StagedDiffApplied   bool     `json:"staged_diff_applied,omitempty"`
	UnstagedDiffApplied bool     `json:"unstaged_diff_applied,omitempty"`
	UntrackedWritten    int      `json:"untracked_written,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

// ApplyCode transports the prepared code state into a repository. Planning is
// read-only; --apply fetches the bundled commit, checks it out (creating a
// backup ref and stashing local changes first), then restores staged/unstaged
// diffs and untracked bytes, verifying every hash. It never executes package
// content.
func ApplyCode(pkg, repo string, dryRun, force bool) (CodeReport, error) {
	v, err := inspectPackage(pkg)
	if err != nil {
		return CodeReport{}, err
	}
	r := CodeReport{PackageDir: pkg, Repo: repo, DryRun: dryRun,
		HandoffID:  v.manifest.Packet.HandoffID,
		PacketHead: v.manifest.Packet.CodeState.Head, PacketBranch: v.manifest.Packet.CodeState.Branch}
	if r.PacketHead == "" {
		return CodeReport{}, fmt.Errorf("package has no prepared code head")
	}

	if err := safeRefComponent(v.manifest.Packet.HandoffID); err != nil {
		return CodeReport{}, fmt.Errorf("unsafe handoff id %q: %w", v.manifest.Packet.HandoffID, err)
	}
	if b := v.manifest.Packet.CodeState.Branch; b != "" && b != "detached" {
		if err := safeBranchName(b); err != nil {
			return CodeReport{}, fmt.Errorf("unsafe branch name %q in the packet: %w", b, err)
		}
	}
	bundleRel := ""
	for _, f := range v.manifest.Files {
		if f.Kind == "git_bundle" {
			bundleRel = f.Path
			r.BundleIncluded = true
			r.BundleSHA256 = f.SHA256
			break
		}
	}
	if bundleRel == "" {
		return CodeReport{}, fmt.Errorf("package carries no code bundle; export with --include-code")
	}
	bundlePath := filepath.Join(pkg, filepath.FromSlash(bundleRel))
	r.BundlePath = bundlePath

	if out, err := git(repo, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(string(out)) != "true" {
		return CodeReport{}, fmt.Errorf("target %s is not a git working tree", repo)
	}
	if out, err := git(repo, "rev-parse", "HEAD"); err == nil {
		r.RepoHead = strings.TrimSpace(string(out))
	}
	if out, err := git(repo, "status", "--porcelain"); err != nil {
		return CodeReport{}, fmt.Errorf("cannot read the target working tree: %w", err)
	} else if strings.TrimSpace(string(out)) != "" {
		if force {
			r.Actions = append(r.Actions, "stash local changes as an anchor backup before checking out")
		} else {
			r.Blockers = append(r.Blockers, "target working tree is dirty; --force is required (local changes are stashed and a backup ref is created)")
		}
	}

	if _, err := git(repo, "bundle", "verify", bundlePath); err != nil {
		return CodeReport{}, fmt.Errorf("code bundle failed verification: %w", err)
	}
	r.BundleVerified = true
	heads, err := git(repo, "bundle", "list-heads", bundlePath)
	if err != nil {
		return CodeReport{}, fmt.Errorf("cannot inspect the code bundle: %w", err)
	}
	if !bundleContainsHead(string(heads), r.PacketHead) {
		return CodeReport{}, fmt.Errorf("code bundle does not contain the prepared commit %s", r.PacketHead)
	}

	// Untracked bytes must not silently overwrite local files.
	untracked := untrackedPayloads(v.manifest.Packet)
	for _, u := range untracked {
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(u.Path))); err == nil {
			r.Blockers = append(r.Blockers, "untracked path already exists in the target: "+u.Path)
		}
	}
	sort.Strings(r.Actions)
	r.Actions = append(r.Actions,
		"fetch the prepared commit from the bundle",
		"check out commit "+r.PacketHead,
	)
	if len(untracked) > 0 {
		r.Actions = append(r.Actions, fmt.Sprintf("write %d untracked file(s)", len(untracked)))
	}
	if hasKind(v.manifest.Files, "git_diff") {
		r.Actions = append(r.Actions, "apply staged/unstaged diffs included in the package")
	}

	if dryRun {
		return r, nil
	}
	if len(r.Blockers) > 0 {
		return CodeReport{}, fmt.Errorf("%s", strings.Join(r.Blockers, "; "))
	}

	// Stash first so a force-apply never loses local work.
	if r.RepoHead != "" {
		ref := "refs/anchor/backup/" + time.Now().UTC().Format("20060102T150405.000000000")
		if _, err := git(repo, "update-ref", ref, "HEAD"); err != nil {
			return CodeReport{}, fmt.Errorf("cannot create a backup ref: %w", err)
		}
		r.BackupRef = ref
	}
	if out, err := git(repo, "status", "--porcelain"); err == nil && strings.TrimSpace(string(out)) != "" {
		if !force {
			return CodeReport{}, fmt.Errorf("target working tree is dirty; --force is required")
		}
		if _, err := git(repo, "stash", "push", "--include-untracked", "-m", "anchor pre-apply backup "+r.HandoffID); err != nil {
			return CodeReport{}, fmt.Errorf("cannot stash local changes: %w", err)
		}
		r.Stashed = true
		if out, err := git(repo, "rev-parse", "stash@{0}"); err == nil {
			r.StashRef = strings.TrimSpace(string(out))
			r.Warnings = append(r.Warnings, "local changes were stashed at "+r.StashRef)
		}
	}

	if _, err := git(repo, "fetch", bundlePath, "HEAD:refs/anchor/handoff/"+r.HandoffID); err != nil {
		return CodeReport{}, fmt.Errorf("cannot fetch from the code bundle: %w", err)
	}
	if r.PacketBranch != "" && r.PacketBranch != "detached" {
		if _, err := git(repo, "checkout", "-B", r.PacketBranch, r.PacketHead); err != nil {
			return CodeReport{}, fmt.Errorf("cannot check out %s: %w", r.PacketBranch, err)
		}
	} else if _, err := git(repo, "checkout", "--detach", r.PacketHead); err != nil {
		return CodeReport{}, fmt.Errorf("cannot check out the prepared commit: %w", err)
	}
	if out, err := git(repo, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(string(out)) != r.PacketHead {
		return CodeReport{}, fmt.Errorf("checked out HEAD does not match the prepared commit")
	}

	// Every mutation after the checkout is wrapped: a failure rolls the worktree
	// back to the pre-apply commit instead of leaving it half-restored.
	run := func(fn func() error) error {
		if err := fn(); err != nil {
			if note := rollbackCode(repo, r.RepoHead); note != "" {
				return fmt.Errorf("%w; %s", err, note)
			}
			return err
		}
		return nil
	}
	if rel, ok := payloadPath(v, "payload/git/staged.diff"); ok {
		path := filepath.Join(pkg, filepath.FromSlash(rel))
		if err := checkDiffHash(path, v.manifest.Packet.CodeState.StagedDiffSHA256); err != nil {
			return CodeReport{}, err
		}
		if err := run(func() error { return applyDiffFile(repo, path, true) }); err != nil {
			return CodeReport{}, err
		}
		r.StagedDiffApplied = true
	}
	if rel, ok := payloadPath(v, "payload/git/unstaged.diff"); ok {
		path := filepath.Join(pkg, filepath.FromSlash(rel))
		if err := checkDiffHash(path, v.manifest.Packet.CodeState.UnstagedDiffSHA256); err != nil {
			return CodeReport{}, err
		}
		if err := run(func() error { return applyDiffFile(repo, path, false) }); err != nil {
			return CodeReport{}, err
		}
		r.UnstagedDiffApplied = true
	}
	for _, u := range untracked {
		rel, ok := payloadPath(v, "payload/untracked/"+u.Path)
		if !ok {
			continue
		}
		src := filepath.Join(pkg, filepath.FromSlash(rel))
		if err := checkBlobHash(src, u.SHA256); err != nil {
			return CodeReport{}, err
		}
		if err := run(func() error { return writeUntracked(repo, u.Path, src) }); err != nil {
			return CodeReport{}, err
		}
		r.UntrackedWritten++
	}

	out, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		return CodeReport{}, err
	}
	r.NewHead = strings.TrimSpace(string(out))
	if r.NewHead != r.PacketHead {
		return CodeReport{}, fmt.Errorf("applied code HEAD %s does not match the packet head %s", r.NewHead, r.PacketHead)
	}
	r.Applied = true
	return r, nil
}

func untrackedPayloads(p handoff.Packet) []handoff.FileRef {
	var out []handoff.FileRef
	for _, f := range p.CodeState.Untracked {
		if f.SHA256 != "" && !f.Skipped {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func hasKind(files []FileEntry, kind string) bool {
	for _, f := range files {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func payloadPath(v verifiedPackage, want string) (string, bool) {
	for _, f := range v.manifest.Files {
		if f.Path == want {
			return f.Path, true
		}
	}
	return "", false
}

func applyDiffFile(repo, path string, staged bool) error {
	data, err := core.ReadRegularFileLimited(path, MaxFileBytes)
	if err != nil {
		return err
	}
	if err := core.ScanSecrets(data); err != nil {
		return fmt.Errorf("diff rejected: %w", err)
	}
	tmp, err := os.CreateTemp("", "anchor-apply-*.diff")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	args := []string{"apply", "--binary"}
	if staged {
		args = append(args, "--index")
	}
	args = append(args, tmp.Name())
	if _, err := git(repo, args...); err != nil {
		return fmt.Errorf("cannot apply the packaged diff: %w", err)
	}
	return nil
}

func writeUntracked(repo, rel, src string) error {
	if err := handoff.CheckPath(rel); err != nil {
		return fmt.Errorf("unsafe untracked path %q", rel)
	}
	data, err := core.ReadRegularFileLimited(src, MaxFileBytes)
	if err != nil {
		return err
	}
	if err := core.ScanSecrets(data); err != nil {
		return fmt.Errorf("untracked file rejected: %w", err)
	}
	dst := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write untracked file %s: %w", rel, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// safeRefComponent rejects values that would make a malformed git refname.
func safeRefComponent(s string) error {
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
		return fmt.Errorf("empty, dot-only or dash-prefixed value")
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("only letters, digits, dot, dash and underscore are allowed")
		}
	}
	return nil
}

// safeBranchName rejects branch names that git would treat as options or would
// otherwise refuse, since the packet supplies the name.
func safeBranchName(name string) error {
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "//") {
		return fmt.Errorf("invalid branch name")
	}
	if strings.ContainsAny(name, " ~^:?*[\\\x00") {
		return fmt.Errorf("invalid characters in branch name")
	}
	return nil
}

// checkDiffHash verifies that a packaged diff matches the packet's recorded hash.
func checkDiffHash(path, want string) error {
	if want == "" {
		return nil
	}
	data, err := core.ReadRegularFileLimited(path, MaxFileBytes)
	if err != nil {
		return err
	}
	if got := hashBytes(data); got != want && got != "sha256:"+strings.TrimPrefix(want, "sha256:") {
		return fmt.Errorf("packaged diff %s does not match the packet hash", filepath.Base(path))
	}
	return nil
}

// checkBlobHash verifies that a packaged untracked file matches the recorded hash.
func checkBlobHash(path, want string) error {
	if want == "" {
		return nil
	}
	data, err := core.ReadRegularFileLimited(path, MaxFileBytes)
	if err != nil {
		return err
	}
	if got := hashBytes(data); got != "sha256:"+strings.TrimPrefix(want, "sha256:") {
		return fmt.Errorf("packaged untracked file %s does not match the packet hash", filepath.Base(path))
	}
	return nil
}

// rollbackCode restores the pre-apply commit after a partial application and
// returns a human-readable note for the error.
func rollbackCode(repo, oldHead string) string {
	if oldHead == "" {
		return "the target repository had no previous commit, so the worktree may be partially modified"
	}
	if _, err := git(repo, "reset", "--hard", oldHead); err != nil {
		return "rollback to " + oldHead + " also failed: " + err.Error()
	}
	return "the worktree was rolled back to " + oldHead
}
