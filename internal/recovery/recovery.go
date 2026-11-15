package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
	"github.com/JingxuanC/anchor/internal/pathguard"
)

const (
	SchemaVersion       = 1
	MaxFileBytes        = 64 << 20
	MaxTotalBytes int64 = 256 << 20
	MaxFiles            = 1000
)

type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"`
}

type Manifest struct {
	SchemaVersion          int            `json:"schema_version"`
	ExportID               string         `json:"export_id"`
	CreatedAt              time.Time      `json:"created_at"`
	WorkspaceID            string         `json:"workspace_id"`
	DeviceID               string         `json:"device_id"`
	EventLogVersion        int            `json:"event_log_version"`
	Packet                 handoff.Packet `json:"packet"`
	ArtifactAvailability   string         `json:"artifact_availability"`
	RecoveryStatus         string         `json:"recovery_status"`
	CodeApply              string         `json:"code_apply"`
	AdapterNativeRecovery  string         `json:"adapter_native_recovery"`
	CodeCommitIncluded     bool           `json:"code_commit_included"`
	StagedDiffIncluded     bool           `json:"staged_diff_included"`
	UnstagedDiffIncluded   bool           `json:"unstaged_diff_included"`
	UntrackedBytesIncluded int            `json:"untracked_bytes_included"`
	EvidenceBytesIncluded  int            `json:"evidence_bytes_included"`
	EventBatches           int            `json:"event_batches,omitempty"`
	Files                  []FileEntry    `json:"files,omitempty"`
	Warnings               []string       `json:"warnings,omitempty"`
	ContentHash            string         `json:"content_hash"`
}

type Report struct {
	DryRun                bool     `json:"dry_run"`
	PackageDir            string   `json:"package_dir"`
	Target                string   `json:"target"`
	WorkspaceID           string   `json:"workspace_id"`
	DeviceID              string   `json:"device_id"`
	RecoveryStatus        string   `json:"recovery_status"`
	ArtifactAvailability  string   `json:"artifact_availability"`
	CodeApply             string   `json:"code_apply"`
	AdapterNativeRecovery string   `json:"adapter_native_recovery"`
	FilesVerified         int      `json:"files_verified"`
	BytesVerified         int64    `json:"bytes_verified"`
	FilesWritten          int      `json:"files_written"`
	Writes                int      `json:"writes"`
	DoesNotExecute        bool     `json:"does_not_execute"`
	AutoInstall           bool     `json:"auto_install"`
	Upload                bool     `json:"upload"`
	WorkspaceRebuilt      bool     `json:"workspace_rebuilt"`
	EventBatches          int      `json:"event_batches,omitempty"`
	EventsApplied         int      `json:"events_applied,omitempty"`
	EvidenceVerified      int      `json:"evidence_verified,omitempty"`
	ProjectionStatus      string   `json:"projection_status,omitempty"`
	Warnings              []string `json:"warnings,omitempty"`
}

type verifiedPackage struct {
	manifest Manifest
	data     map[string][]byte
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newID() string {
	return fmt.Sprintf("exp_%d", time.Now().UTC().UnixNano())
}

func workspaceIdentity(dir string) (string, string, int, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", "", 0, err
	}
	var m struct {
		WorkspaceID     string `json:"workspace_id"`
		DeviceID        string `json:"device_id"`
		EventLogVersion int    `json:"event_log_version"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", "", 0, err
	}
	return m.WorkspaceID, m.DeviceID, m.EventLogVersion, nil
}

func safePackagePath(rel string) error {
	if err := handoff.CheckPath(rel); err != nil {
		return fmt.Errorf("unsafe package path %q", rel)
	}
	if !strings.HasPrefix(rel, "payload/") {
		return fmt.Errorf("package file must be under payload/: %q", rel)
	}
	return nil
}

func writePackageFile(staging, rel, kind string, data []byte, files *[]FileEntry) error {
	if err := safePackagePath(rel); err != nil {
		return err
	}
	if err := core.ScanSecrets([]byte(rel)); err != nil {
		return fmt.Errorf("package path rejected: %w", err)
	}
	if len(data) > MaxFileBytes {
		return fmt.Errorf("package file exceeds %d bytes", MaxFileBytes)
	}
	if err := core.ScanSecrets(data); err != nil {
		return fmt.Errorf("package file %s rejected: %w", rel, err)
	}
	dst := filepath.Join(staging, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	*files = append(*files, FileEntry{Path: rel, SHA256: hashBytes(data), Size: int64(len(data)), Kind: kind})
	return nil
}

func git(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Export is explicit and local-only. The canonical workspace state (workspace
// manifest and event log) is always transported, so an import can rebuild the
// workspace. Artifact bytes are included only when includeBytes is true, and
// Git commit bytes are never included.
func Export(dir, repo, out string, includeBytes bool) (Manifest, string, error) {
	store, err := core.Open(dir)
	if err != nil {
		return Manifest{}, "", err
	}
	wsID, devID, version, err := workspaceIdentity(store.Dir)
	if err != nil {
		return Manifest{}, "", err
	}
	p, _, err := handoff.Prepare(store.Dir, repo)
	if err != nil {
		return Manifest{}, "", err
	}
	if _, err := os.Lstat(out); err == nil {
		return Manifest{}, "", fmt.Errorf("refusing to overwrite existing export target %s", out)
	} else if !os.IsNotExist(err) {
		return Manifest{}, "", err
	}
	canonical, err := pathguard.ValidateCreateDir(out)
	if err != nil {
		return Manifest{}, "", err
	}
	if _, err := os.Lstat(canonical); err == nil {
		return Manifest{}, "", fmt.Errorf("refusing to overwrite existing export target %s", canonical)
	} else if !os.IsNotExist(err) {
		return Manifest{}, "", err
	}
	parent := filepath.Dir(canonical)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return Manifest{}, "", fmt.Errorf("export parent directory does not exist")
	}
	staging, err := os.MkdirTemp(parent, ".anchor-export-*")
	if err != nil {
		return Manifest{}, "", err
	}
	defer os.RemoveAll(staging)

	m := Manifest{SchemaVersion: SchemaVersion, ExportID: newID(), CreatedAt: time.Now().UTC(), WorkspaceID: wsID, DeviceID: devID, EventLogVersion: version, Packet: p, ArtifactAvailability: "metadata_only", RecoveryStatus: "complete", CodeApply: "disabled", AdapterNativeRecovery: "unverified"}
	m.Warnings = append(m.Warnings, p.Warnings...)
	var files []FileEntry
	markIncomplete := func(reason string) {
		m.RecoveryStatus = handoff.StatusIncomplete
		m.Warnings = append(m.Warnings, reason)
	}

	// Canonical state is always part of the package: without the event log an
	// import can only drop bytes into an empty directory, never rebuild a
	// workspace.
	wsManifest, err := core.ReadRegularFileLimited(filepath.Join(store.Dir, "manifest.json"), MaxFileBytes)
	if err != nil {
		return Manifest{}, "", err
	}
	if err := writePackageFile(staging, "payload/workspace/manifest.json", "workspace_manifest", wsManifest, &files); err != nil {
		return Manifest{}, "", err
	}
	batches, err := store.EventBatchPaths()
	if err != nil {
		return Manifest{}, "", err
	}
	for _, batchPath := range batches {
		data, err := core.ReadRegularFileLimited(batchPath, MaxFileBytes)
		if err != nil {
			return Manifest{}, "", err
		}
		name := filepath.Base(batchPath)
		if err := writePackageFile(staging, "payload/events/"+name, "event", data, &files); err != nil {
			return Manifest{}, "", err
		}
		m.EventBatches++
	}

	if len(p.Quarantined) > 0 {
		// Quarantined batches are transported verbatim; recovery stays incomplete
		// because their events cannot be replayed or trusted.
		markIncomplete("some event batches are quarantined; recovery is incomplete")
	}
	if p.Tree.ProjectionStatus != "complete" {
		markIncomplete("projection has pending or conflicting events")
	}
	if p.CodeState.Dirty {
		markIncomplete("uncommitted code is not transported; code apply is disabled")
	}
	if len(p.Tree.Evidence) > 0 && !includeBytes {
		markIncomplete("evidence bytes were not included; evidence cannot be verified after import")
	}

	if includeBytes {
		m.ArtifactAvailability = "partial"
		var evidenceOK, untrackedOK bool = true, true
		writtenEvidence := map[string]bool{}
		for _, ev := range p.Tree.Evidence {
			if err := core.VerifyEvidenceArtifact(store.Dir, ev); err != nil {
				m.Warnings = append(m.Warnings, "evidence "+ev.EvidenceID+" unavailable: "+err.Error())
				evidenceOK = false
				continue
			}
			data, err := core.ReadRegularFileLimited(filepath.Join(store.Dir, "evidence", "blobs", ev.SHA256), core.MaxEvidenceBytes)
			if err != nil {
				m.Warnings = append(m.Warnings, "evidence "+ev.EvidenceID+" unreadable: "+err.Error())
				evidenceOK = false
				continue
			}
			if strings.TrimPrefix(hashBytes(data), "sha256:") != ev.SHA256 {
				m.Warnings = append(m.Warnings, "evidence hash changed: "+ev.EvidenceID)
				evidenceOK = false
				continue
			}
			// Evidence is content-addressed: several evidence ids can share one blob.
			// Write each blob once instead of failing on the duplicate path.
			if !writtenEvidence[ev.SHA256] {
				if err := writePackageFile(staging, "payload/evidence/"+ev.SHA256, "evidence", data, &files); err != nil {
					return Manifest{}, "", err
				}
				writtenEvidence[ev.SHA256] = true
				m.EvidenceBytesIncluded++
			}
		}
		for _, f := range p.CodeState.Untracked {
			if f.SHA256 == "" || f.Skipped {
				m.Warnings = append(m.Warnings, "untracked bytes unavailable: "+f.Path)
				untrackedOK = false
				continue
			}
			if err := handoff.CheckPath(f.Path); err != nil {
				return Manifest{}, "", fmt.Errorf("unsafe untracked path %q", f.Path)
			}
			data, err := core.ReadRegularFileLimited(filepath.Join(repo, filepath.FromSlash(f.Path)), MaxFileBytes)
			if err != nil {
				m.Warnings = append(m.Warnings, "untracked bytes unreadable: "+f.Path)
				untrackedOK = false
				continue
			}
			if hashBytes(data) != f.SHA256 {
				m.Warnings = append(m.Warnings, "untracked hash changed: "+f.Path)
				untrackedOK = false
				continue
			}
			if err := writePackageFile(staging, "payload/untracked/"+f.Path, "untracked", data, &files); err != nil {
				return Manifest{}, "", err
			}
			m.UntrackedBytesIncluded++
		}
		if data, err := git(repo, "diff", "--cached", "--binary"); err == nil && len(data) > 0 {
			if p.CodeState.StagedDiffSHA256 != "" && hashBytes(data) != p.CodeState.StagedDiffSHA256 {
				m.Warnings = append(m.Warnings, "staged diff hash changed; bytes excluded")
			} else if err := writePackageFile(staging, "payload/git/staged.diff", "git_diff", data, &files); err != nil {
				return Manifest{}, "", err
			} else {
				m.StagedDiffIncluded = true
			}
		} else if p.CodeState.StagedCount > 0 {
			m.Warnings = append(m.Warnings, "staged diff bytes unavailable")
		}
		if data, err := git(repo, "diff", "--binary"); err == nil && len(data) > 0 {
			if p.CodeState.UnstagedDiffSHA256 != "" && hashBytes(data) != p.CodeState.UnstagedDiffSHA256 {
				m.Warnings = append(m.Warnings, "unstaged diff hash changed; bytes excluded")
			} else if err := writePackageFile(staging, "payload/git/unstaged.diff", "git_diff", data, &files); err != nil {
				return Manifest{}, "", err
			} else {
				m.UnstagedDiffIncluded = true
			}
		} else if p.CodeState.UnstagedCount > 0 {
			m.Warnings = append(m.Warnings, "unstaged diff bytes unavailable")
		}
		if !evidenceOK || !untrackedOK {
			markIncomplete("some selected bytes were unavailable")
		}
		if len(files) == 0 {
			m.ArtifactAvailability = "missing"
		}
	}

	m.Warnings = append(m.Warnings, "git commit bytes are not included; the successor must check out the recorded commit and code apply stays disabled")
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	m.Files = files
	// Import enforces these same limits; fail here instead of producing a package
	// that cannot be imported.
	var packageBytes int64
	for _, f := range files {
		packageBytes += f.Size
	}
	if len(files) > MaxFiles {
		return Manifest{}, "", fmt.Errorf("workspace needs %d package files, above the %d-file package limit", len(files), MaxFiles)
	}
	if packageBytes > MaxTotalBytes {
		return Manifest{}, "", fmt.Errorf("workspace needs %d bytes, above the %d-byte package limit", packageBytes, MaxTotalBytes)
	}
	if len(m.Warnings) == 0 {
		m.Warnings = nil
	}
	clone := m
	clone.ContentHash = ""
	b, err := json.Marshal(clone)
	if err != nil {
		return Manifest{}, "", err
	}
	m.ContentHash = hashBytes(b)
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	if err := core.ScanSecrets(mb); err != nil {
		return Manifest{}, "", fmt.Errorf("export manifest rejected: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), append(mb, '\n'), 0o600); err != nil {
		return Manifest{}, "", err
	}
	if err := os.Rename(staging, canonical); err != nil {
		return Manifest{}, "", err
	}
	return m, canonical, nil
}

func inspectPackage(pkg string) (verifiedPackage, error) {
	canonical, err := pathguard.ValidateExistingDir(pkg)
	if err != nil {
		return verifiedPackage{}, err
	}
	mbPath := filepath.Join(canonical, "manifest.json")
	info, err := os.Lstat(mbPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return verifiedPackage{}, fmt.Errorf("package manifest missing or not a regular file")
	}
	mb, err := core.ReadRegularFileLimited(mbPath, MaxFileBytes)
	if err != nil {
		return verifiedPackage{}, err
	}
	if err := core.ScanSecrets(mb); err != nil {
		return verifiedPackage{}, fmt.Errorf("package manifest rejected: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return verifiedPackage{}, err
	}
	if m.SchemaVersion != SchemaVersion || m.ExportID == "" {
		return verifiedPackage{}, fmt.Errorf("unsupported package manifest")
	}
	clone := m
	clone.ContentHash = ""
	b, _ := json.Marshal(clone)
	if m.ContentHash != hashBytes(b) {
		return verifiedPackage{}, fmt.Errorf("package content hash mismatch")
	}
	if len(m.Files) > MaxFiles {
		return verifiedPackage{}, fmt.Errorf("package has too many files")
	}
	data := map[string][]byte{}
	allowed := map[string]bool{"manifest.json": true}
	var total int64
	for _, f := range m.Files {
		if err := safePackagePath(f.Path); err != nil {
			return verifiedPackage{}, err
		}
		if allowed[f.Path] {
			return verifiedPackage{}, fmt.Errorf("duplicate package path %q", f.Path)
		}
		allowed[f.Path] = true
		p := filepath.Join(canonical, filepath.FromSlash(f.Path))
		info, err := os.Lstat(p)
		if err != nil {
			return verifiedPackage{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return verifiedPackage{}, fmt.Errorf("package file must be regular and not a symlink: %s", f.Path)
		}
		if info.Size() != f.Size || f.Size > MaxFileBytes {
			return verifiedPackage{}, fmt.Errorf("package file size mismatch: %s", f.Path)
		}
		fb, err := core.ReadRegularFileLimited(p, MaxFileBytes)
		if err != nil {
			return verifiedPackage{}, err
		}
		if hashBytes(fb) != f.SHA256 {
			return verifiedPackage{}, fmt.Errorf("package file hash mismatch: %s", f.Path)
		}
		if err := core.ScanSecrets(fb); err != nil {
			return verifiedPackage{}, fmt.Errorf("package file rejected: %w", err)
		}
		total += int64(len(fb))
		if total > MaxTotalBytes {
			return verifiedPackage{}, fmt.Errorf("package exceeds total byte limit")
		}
		data[f.Path] = fb
	}
	err = filepath.WalkDir(canonical, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == canonical {
			return nil
		}
		rel, _ := filepath.Rel(canonical, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("package contains a symlink")
		}
		if !allowed[rel] {
			return fmt.Errorf("package contains unlisted file %q", rel)
		}
		return nil
	})
	if err != nil {
		return verifiedPackage{}, err
	}
	return verifiedPackage{manifest: m, data: data}, nil
}

// importPlan classifies the verified payload of a package.
type importPlan struct {
	workspaceManifest []byte
	hasWorkspace      bool
	events            map[string][]byte // batch file name -> bytes
	evidence          map[string][]byte // sha256 -> bytes
	artifacts         map[string][]byte // path under payload/ -> bytes (untracked, git diffs)
}

func importPlanOf(v verifiedPackage) importPlan {
	plan := importPlan{events: map[string][]byte{}, evidence: map[string][]byte{}, artifacts: map[string][]byte{}}
	for path, data := range v.data {
		switch {
		case path == "payload/workspace/manifest.json":
			plan.workspaceManifest = data
		case strings.HasPrefix(path, "payload/events/"):
			plan.events[strings.TrimPrefix(path, "payload/events/")] = data
		case strings.HasPrefix(path, "payload/evidence/"):
			plan.evidence[strings.TrimPrefix(path, "payload/evidence/")] = data
		case strings.HasPrefix(path, "payload/"):
			plan.artifacts[strings.TrimPrefix(path, "payload/")] = data
		}
	}
	// A workspace manifest is enough to rebuild identity; a freshly initialized
	// workspace legitimately has no event batches yet. Packages without it are
	// legacy artifact-only packages that cannot rebuild a workspace.
	plan.hasWorkspace = len(plan.workspaceManifest) > 0
	return plan
}

// assessImport predicts the import outcome from the verified manifest without
// writing anything, so a dry run reports the same status as an applied import
// would (barring a filesystem error).
func assessImport(v verifiedPackage, plan importPlan) (string, []string) {
	if !plan.hasWorkspace {
		return handoff.StatusIncomplete, []string{"package carries no workspace state (no event log or workspace manifest); only verified payload bytes can be written"}
	}
	var reasons []string
	if v.manifest.Packet.Tree.ProjectionStatus != "complete" {
		reasons = append(reasons, "exported projection is not complete")
	}
	if len(v.manifest.Packet.Quarantined) > 0 {
		reasons = append(reasons, "package contains quarantined event batches; recovery is incomplete")
	}
	if v.manifest.Packet.CodeState.Dirty {
		reasons = append(reasons, "uncommitted code was not transported; code apply is disabled")
	}
	uniqueEvidence := map[string]bool{}
	for _, ev := range v.manifest.Packet.Tree.Evidence {
		uniqueEvidence[ev.SHA256] = true
	}
	if len(plan.evidence) < len(uniqueEvidence) {
		reasons = append(reasons, "evidence bytes are missing; evidence cannot be verified after import")
	}
	if len(reasons) > 0 {
		return handoff.StatusIncomplete, reasons
	}
	return handoff.StatusComplete, nil
}

func writeExclusive(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func safePackageBase(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\:\x00") {
		return fmt.Errorf("unsafe package entry name %q", name)
	}
	return nil
}

// Import verifies the package first. Dry-run performs no writes. An applied
// import rebuilds the workspace (manifest, event log, evidence blobs) in an
// empty explicit target, then replays it to report an honest recovery status.
// It never executes package content and never applies code.
func Import(pkg, target string, dryRun bool) (Report, error) {
	v, err := inspectPackage(pkg)
	if err != nil {
		return Report{}, err
	}
	if info, err := os.Lstat(target); err == nil && !info.IsDir() {
		return Report{}, fmt.Errorf("import target is not a directory")
	}
	canonical, err := pathguard.ValidateCreateDir(target)
	if err != nil {
		return Report{}, err
	}
	if info, err := os.Lstat(canonical); err == nil {
		if !info.IsDir() {
			return Report{}, fmt.Errorf("import target is not a directory")
		}
		entries, err := os.ReadDir(canonical)
		if err != nil {
			return Report{}, err
		}
		if len(entries) != 0 {
			return Report{}, fmt.Errorf("import target must be empty")
		}
	} else if !os.IsNotExist(err) {
		return Report{}, err
	}
	// The target parent must exist for both dry-run and apply, matching export, so
	// a dry run never predicts success for a target that cannot be created.
	parent := filepath.Dir(canonical)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return Report{}, fmt.Errorf("import target parent directory does not exist")
	}
	plan := importPlanOf(v)
	status, reasons := assessImport(v, plan)
	r := Report{DryRun: dryRun, PackageDir: pkg, Target: canonical, WorkspaceID: v.manifest.WorkspaceID, DeviceID: v.manifest.DeviceID,
		RecoveryStatus: status, ArtifactAvailability: v.manifest.ArtifactAvailability, CodeApply: "disabled", AdapterNativeRecovery: "unverified",
		FilesVerified: len(v.data), BytesVerified: totalBytes(v.data), DoesNotExecute: true, AutoInstall: false, Upload: false,
		WorkspaceRebuilt: plan.hasWorkspace, EventBatches: len(plan.events), Warnings: reasons}
	r.Warnings = append(r.Warnings, "code apply is disabled; Git commit/dirty worktree restoration is incomplete")
	if dryRun {
		return r, nil
	}
	// Build the import in a staging directory next to the target and rename it
	// into place only after verification passes. A failed import therefore never
	// leaves a half-built workspace that a retry would refuse to touch.
	staging, err := os.MkdirTemp(parent, ".anchor-import-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(staging)

	if !plan.hasWorkspace {
		// Legacy package: mirror the verified payload bytes and report honestly
		// that no workspace was rebuilt.
		for path, data := range v.data {
			if err := writeExclusive(filepath.Join(staging, filepath.FromSlash(path)), data); err != nil {
				return Report{}, err
			}
			r.FilesWritten++
		}
		r.Writes = r.FilesWritten
		report, err := writeImportReport(staging, r)
		if err != nil {
			return Report{}, err
		}
		return commitImport(staging, canonical, report)
	}

	if err := writeExclusive(filepath.Join(staging, "manifest.json"), plan.workspaceManifest); err != nil {
		return Report{}, err
	}
	r.FilesWritten++
	if err := os.MkdirAll(filepath.Join(staging, "events"), 0o700); err != nil {
		return Report{}, err
	}
	for name, data := range plan.events {
		if err := safePackageBase(name); err != nil {
			return Report{}, err
		}
		if err := writeExclusive(filepath.Join(staging, "events", name), data); err != nil {
			return Report{}, err
		}
		r.FilesWritten++
	}
	for sha, data := range plan.evidence {
		if err := safePackageBase(sha); err != nil {
			return Report{}, err
		}
		if err := writeExclusive(filepath.Join(staging, "evidence", "blobs", sha), data); err != nil {
			return Report{}, err
		}
		r.FilesWritten++
	}
	for rel, data := range plan.artifacts {
		if err := handoff.CheckPath(rel); err != nil {
			return Report{}, fmt.Errorf("unsafe artifact path %q", rel)
		}
		// Imported code bytes are device-local staging, not canonical workspace
		// state: keep them under local/ so they are never synced or mistaken for
		// applied code.
		if err := writeExclusive(filepath.Join(staging, "local", "imported", filepath.FromSlash(rel)), data); err != nil {
			return Report{}, err
		}
		r.FilesWritten++
	}

	// Verify the rebuilt workspace instead of trusting the package labels.
	wsID, _, _, err := workspaceIdentity(staging)
	if err != nil {
		return Report{}, fmt.Errorf("rebuilt workspace manifest unreadable: %w", err)
	}
	if v.manifest.WorkspaceID != "" && wsID != v.manifest.WorkspaceID {
		return Report{}, fmt.Errorf("rebuilt workspace identity %q does not match the package workspace %q", wsID, v.manifest.WorkspaceID)
	}
	store, err := core.Open(staging)
	if err != nil {
		return Report{}, fmt.Errorf("rebuilt workspace is not openable: %w", err)
	}
	load, err := store.LoadReport()
	if err != nil {
		return Report{}, err
	}
	replay, err := store.LoadReplay()
	if err != nil {
		return Report{}, err
	}
	missingEvidence := 0
	for _, ev := range replay.Tree.Evidence {
		if err := core.VerifyEvidenceArtifact(staging, ev); err != nil {
			missingEvidence++
		}
	}
	r.EventsApplied = len(replay.Applied)
	r.ProjectionStatus = replay.RecoveryStatus
	r.EvidenceVerified = len(replay.Tree.Evidence) - missingEvidence

	complete := true
	if len(load.Quarantined) > 0 {
		complete = false
		r.Warnings = append(r.Warnings, "rebuilt workspace has quarantined event batches")
	}
	if replay.RecoveryStatus != "complete" {
		complete = false
		r.Warnings = append(r.Warnings, "rebuilt projection is not complete (pending events or conflicts)")
	}
	if missingEvidence > 0 {
		complete = false
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d evidence artifacts could not be verified", missingEvidence))
	}
	if v.manifest.Packet.CodeState.Dirty {
		complete = false
	}
	if complete {
		r.RecoveryStatus = handoff.StatusComplete
	} else {
		r.RecoveryStatus = handoff.StatusIncomplete
	}
	r.Writes = r.FilesWritten
	report, err := writeImportReport(staging, r)
	if err != nil {
		return Report{}, err
	}
	return commitImport(staging, canonical, report)
}

// commitImport moves a verified staging directory into the requested target.
func commitImport(staging, canonical string, r Report) (Report, error) {
	if err := os.Rename(staging, canonical); err != nil {
		// The target may already exist as an empty directory.
		if entries, rerr := os.ReadDir(canonical); rerr == nil && len(entries) == 0 {
			if rerr := os.Remove(canonical); rerr == nil {
				err = os.Rename(staging, canonical)
			}
		}
		if err != nil {
			return Report{}, err
		}
	}
	return r, nil
}

func writeImportReport(canonical string, r Report) (Report, error) {
	// Count the report write before marshalling so the persisted report and the
	// returned report agree (review finding: the on-disk copy was one too low).
	r.Writes++
	rb, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return Report{}, err
	}
	if err := writeExclusive(filepath.Join(canonical, "anchor-import.json"), append(rb, '\n')); err != nil {
		return Report{}, err
	}
	return r, nil
}

func totalBytes(m map[string][]byte) int64 {
	var n int64
	for _, b := range m {
		n += int64(len(b))
	}
	return n
}
