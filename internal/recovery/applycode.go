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
	pkgDir := v.canonical
	packet := v.manifest.Packet
	r := CodeReport{PackageDir: pkgDir, Repo: repo, DryRun: dryRun,
		HandoffID: packet.HandoffID, PacketHead: packet.CodeState.Head, PacketBranch: packet.CodeState.Branch}
	if r.PacketHead == "" {
		return CodeReport{}, fmt.Errorf("package has no prepared code head")
	}
	if err := safeRefComponent(r.HandoffID); err != nil {
		return CodeReport{}, fmt.Errorf("unsafe handoff id %q: %w", r.HandoffID, err)
	}
	if b := r.PacketBranch; b != "" && b != "detached" {
		if err := safeBranchName(b); err != nil {
			return CodeReport{}, fmt.Errorf("unsafe branch name %q in the packet: %w", b, err)
		}
	}

	bundleRel, bundleSHA := "", ""
	for _, f := range v.manifest.Files {
		if f.Kind == "git_bundle" {
			bundleRel, bundleSHA = f.Path, f.SHA256
			r.BundleIncluded = true
			break
		}
	}
	if bundleRel == "" {
		return CodeReport{}, fmt.Errorf("package carries no code bundle; export with --include-code")
	}
	bundlePath := filepath.Join(pkgDir, filepath.FromSlash(bundleRel))
	r.BundlePath, r.BundleSHA256 = bundlePath, bundleSHA

	// The package was verified through its canonical path; re-hash the bundle
	// immediately before use so a path swap after verification cannot be applied.
	if got, err := hashFile(bundlePath); err != nil {
		return CodeReport{}, err
	} else if got != bundleSHA {
		return CodeReport{}, fmt.Errorf("code bundle hash changed after package verification")
	}

	if out, err := git(repo, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(string(out)) != "true" {
		return CodeReport{}, fmt.Errorf("target %s is not a git working tree", repo)
	}
	if out, err := git(repo, "rev-parse", "HEAD"); err == nil {
		r.RepoHead = strings.TrimSpace(string(out))
	}
	dirty := false
	if out, err := git(repo, "status", "--porcelain"); err != nil {
		return CodeReport{}, fmt.Errorf("cannot read the target working tree: %w", err)
	} else if strings.TrimSpace(string(out)) != "" {
		dirty = true
		if force {
			r.Actions = append(r.Actions, "stash local changes as an anchor backup before checking out")
		} else {
			r.Blockers = append(r.Blockers, "target working tree is dirty; --force is required (local changes are stashed and a backup ref is created)")
		}
	}
	if dirty && r.RepoHead == "" {
		r.Blockers = append(r.Blockers, "target repository has no commits yet, so local changes cannot be stashed; commit or remove them first")
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

	// The package must carry every byte the packet claims; otherwise "applied"
	// would be a lie. Validate all payload hashes before touching the repository.
	if !packageCodeCovered(v) {
		r.Blockers = append(r.Blockers, "package does not carry complete code bytes (code bundle, diffs or untracked files are missing); re-export with --include-code --include-bytes")
	}
	if err := validatePayloads(v); err != nil {
		return CodeReport{}, err
	}
	untracked := untrackedPayloads(packet)
	for _, u := range untracked {
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(u.Path))); err == nil && !force {
			r.Blockers = append(r.Blockers, "untracked path already exists in the target: "+u.Path+" (--force stashes it first)")
		}
	}
	sort.Slice(r.Actions, func(i, j int) bool { return r.Actions[i] < r.Actions[j] })
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
		return r, fmt.Errorf("%s", strings.Join(r.Blockers, "; "))
	}

	// Fetch first: it only adds objects/refs, so collisions with ignored local
	// files can be detected before anything in the worktree changes.
	if _, err := git(repo, "fetch", bundlePath, "HEAD:refs/anchor/handoff/"+r.HandoffID); err != nil {
		return r, fmt.Errorf("cannot fetch from the code bundle: %w", err)
	}
	ignoredConflicts, err := ignoredOverwriteConflicts(repo, r.PacketHead)
	if err != nil {
		return r, err
	}
	if len(ignoredConflicts) > 0 && !force {
		return r, fmt.Errorf("checkout would overwrite ignored local files: %s; move them away or use --force (they are stashed with --all)", strings.Join(ignoredConflicts, ", "))
	}
	if len(ignoredConflicts) > 0 {
		r.Warnings = append(r.Warnings, "ignored local files were stashed first: "+strings.Join(ignoredConflicts, ", "))
	}

	if r.RepoHead != "" {
		ref := "refs/anchor/backup/" + time.Now().UTC().Format("20060102T150405.000000000")
		if _, err := git(repo, "update-ref", ref, "HEAD"); err != nil {
			return r, fmt.Errorf("cannot create a backup ref: %w", err)
		}
		r.BackupRef = ref
	}
	if dirty || len(ignoredConflicts) > 0 {
		if r.RepoHead == "" {
			return r, fmt.Errorf("ignored local files would be overwritten but the repository has no commits, so they cannot be stashed; move them away first: %s", strings.Join(ignoredConflicts, ", "))
		}
		args := []string{"stash", "push", "--include-untracked", "-m", "anchor pre-apply backup " + r.HandoffID}
		if len(ignoredConflicts) > 0 {
			args = []string{"stash", "push", "--all", "-m", "anchor pre-apply backup " + r.HandoffID}
		}
		if _, err := git(repo, args...); err != nil {
			return r, r.errWithRefs(fmt.Errorf("cannot stash local changes: %w", err))
		}
		r.Stashed = true
		if out, err := git(repo, "rev-parse", "stash@{0}"); err == nil {
			r.StashRef = strings.TrimSpace(string(out))
			r.Warnings = append(r.Warnings, "local changes were stashed at "+r.StashRef)
		}
	}

	// Never reset an unrelated local branch: only create it when it is absent.
	branchExists := false
	if r.PacketBranch != "" && r.PacketBranch != "detached" {
		if _, err := git(repo, "rev-parse", "--verify", "refs/heads/"+r.PacketBranch); err == nil {
			branchExists = true
		}
	}
	switch {
	case r.PacketBranch == "" || r.PacketBranch == "detached":
		if _, err := git(repo, "checkout", "--detach", r.PacketHead); err != nil {
			return r, r.errWithRefs(fmt.Errorf("cannot check out the prepared commit: %w", err))
		}
	case branchExists:
		if _, err := git(repo, "checkout", "--detach", r.PacketHead); err != nil {
			return r, r.errWithRefs(fmt.Errorf("cannot check out the prepared commit: %w", err))
		}
		r.Warnings = append(r.Warnings, "local branch "+r.PacketBranch+" already exists; checked out the prepared commit in detached HEAD without resetting it")
	default:
		if _, err := git(repo, "checkout", "-B", r.PacketBranch, r.PacketHead); err != nil {
			return r, r.errWithRefs(fmt.Errorf("cannot check out %s: %w", r.PacketBranch, err))
		}
	}
	if out, err := git(repo, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(string(out)) != r.PacketHead {
		note := rollbackCode(repo, r.RepoHead)
		return r, r.errWithRefs(fmt.Errorf("checked out HEAD does not match the prepared commit; %s", note))
	}

	run := func(fn func() error) error {
		if err := fn(); err != nil {
			if note := rollbackCode(repo, r.RepoHead); note != "" {
				return r.errWithRefs(fmt.Errorf("%w; %s", err, note))
			}
			return err
		}
		return nil
	}
	if rel, ok := payloadPath(v, "payload/git/staged.diff"); ok {
		path := filepath.Join(pkgDir, filepath.FromSlash(rel))
		if err := run(func() error { return applyDiffFile(repo, path, true) }); err != nil {
			return r, err
		}
		r.StagedDiffApplied = true
	}
	if rel, ok := payloadPath(v, "payload/git/unstaged.diff"); ok {
		path := filepath.Join(pkgDir, filepath.FromSlash(rel))
		if err := run(func() error { return applyDiffFile(repo, path, false) }); err != nil {
			return r, err
		}
		r.UnstagedDiffApplied = true
	}
	for _, u := range untracked {
		rel, ok := payloadPath(v, "payload/untracked/"+u.Path)
		if !ok {
			continue
		}
		src := filepath.Join(pkgDir, filepath.FromSlash(rel))
		if err := run(func() error { return writeUntracked(repo, u.Path, src) }); err != nil {
			return r, err
		}
		r.UntrackedWritten++
	}

	out, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		return r, r.errWithRefs(err)
	}
	r.NewHead = strings.TrimSpace(string(out))
	if r.NewHead != r.PacketHead {
		return r, r.errWithRefs(fmt.Errorf("applied code HEAD %s does not match the packet head %s", r.NewHead, r.PacketHead))
	}
	r.Applied = true
	return r, nil
}

// errWithRefs appends the backup/stash locations to an error so a failed apply
// never hides where the local work went.
func (r CodeReport) errWithRefs(err error) error {
	var refs []string
	if r.BackupRef != "" {
		refs = append(refs, "backup ref "+r.BackupRef)
	}
	if r.StashRef != "" {
		refs = append(refs, "stash "+r.StashRef)
	}
	if len(refs) == 0 {
		return err
	}
	return fmt.Errorf("%w (local work preserved in %s)", err, strings.Join(refs, ", "))
}

// validatePayloads checks every packaged code payload against the packet's
// recorded hashes before anything is mutated.
func validatePayloads(v verifiedPackage) error {
	cs := v.manifest.Packet.CodeState
	for _, item := range []struct{ rel, want, label string }{
		{"payload/git/staged.diff", cs.StagedDiffSHA256, "staged diff"},
		{"payload/git/unstaged.diff", cs.UnstagedDiffSHA256, "unstaged diff"},
	} {
		rel, ok := payloadPath(v, item.rel)
		if !ok {
			if (item.label == "staged diff" && cs.StagedCount > 0) || (item.label == "unstaged diff" && cs.UnstagedCount > 0) {
				return fmt.Errorf("package is missing the %s it claims", item.label)
			}
			continue
		}
		if err := checkDiffHash(filepath.Join(v.canonical, filepath.FromSlash(rel)), item.want); err != nil {
			return err
		}
	}
	for _, u := range untrackedPayloads(v.manifest.Packet) {
		rel, ok := payloadPath(v, "payload/untracked/"+u.Path)
		if !ok {
			return fmt.Errorf("package is missing untracked file %s it claims", u.Path)
		}
		if err := checkBlobHash(filepath.Join(v.canonical, filepath.FromSlash(rel)), u.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// ignoredOverwriteConflicts lists ignored local files that the checkout of head
// would silently overwrite (git status hides them, so they need a separate check).
func ignoredOverwriteConflicts(repo, head string) ([]string, error) {
	treeOut, err := git(repo, "ls-tree", "-r", "--name-only", head)
	if err != nil {
		return nil, fmt.Errorf("cannot read the prepared commit tree: %w", err)
	}
	ignoredOut, err := git(repo, "ls-files", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("cannot list ignored files: %w", err)
	}
	inTree := map[string]bool{}
	for _, line := range strings.Split(string(treeOut), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			inTree[line] = true
		}
	}
	var conflicts []string
	for _, line := range strings.Split(string(ignoredOut), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && inTree[line] {
			conflicts = append(conflicts, line)
		}
	}
	sort.Strings(conflicts)
	return conflicts, nil
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

// writeUntracked writes one packaged file into the repository, refusing to
// traverse a symlinked parent directory or to overwrite an existing path.
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
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return fmt.Errorf("cannot resolve the target repository: %w", err)
	}
	// Walk (and create) every parent component, refusing symlinks and non-dirs.
	dir := root
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	for _, part := range strings.Split(parent, "/") {
		if part == "" || part == "." {
			continue
		}
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		switch {
		case os.IsNotExist(err):
			if err := os.Mkdir(dir, 0o700); err != nil {
				return fmt.Errorf("cannot create directory for %s: %w", rel, err)
			}
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("refusing to write through symlinked directory %s", dir)
		case !info.IsDir():
			return fmt.Errorf("refusing to write through non-directory %s", dir)
		}
	}
	final := filepath.Join(dir, filepath.Base(filepath.FromSlash(rel)))
	if rel2, err := filepath.Rel(root, final); err != nil || strings.HasPrefix(rel2, "..") {
		return fmt.Errorf("refusing to write %s outside the target repository", rel)
	}
	f, err := os.OpenFile(final, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	if strings.ContainsAny(name, " ~^:?*[\\\\\\x00") {
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
	if got := hashBytes(data); got != "sha256:"+strings.TrimPrefix(want, "sha256:") {
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
