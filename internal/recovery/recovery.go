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
		return fmt.Errorf("package bytes rejected: %w", err)
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

// Export is explicit and local-only. Bytes are omitted unless includeBytes is
// true. Code commit objects are never included, so import recovery remains
// incomplete even when selected bytes are present.
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
	m := Manifest{SchemaVersion: SchemaVersion, ExportID: newID(), CreatedAt: time.Now().UTC(), WorkspaceID: wsID, DeviceID: devID, EventLogVersion: version, Packet: p, ArtifactAvailability: "metadata_only", RecoveryStatus: "incomplete", CodeApply: "disabled", AdapterNativeRecovery: "unverified"}
	m.Warnings = append(m.Warnings, p.Warnings...)
	m.Warnings = append(m.Warnings, "git commit bytes are not included; code apply is disabled")
	if includeBytes {
		m.ArtifactAvailability = "partial"
		var files []FileEntry
		evidenceOK := true
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
			if err := writePackageFile(staging, "payload/evidence/"+ev.SHA256, "evidence", data, &files); err != nil {
				return Manifest{}, "", err
			}
			m.EvidenceBytesIncluded++
		}
		untrackedOK := true
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
		if len(files) == 0 {
			m.ArtifactAvailability = "missing"
		} else {
			// Even when all selected evidence/untracked/diff bytes are present,
			// Git commit bytes and code apply are unavailable.
			m.ArtifactAvailability = "partial"
			if !evidenceOK || !untrackedOK {
				m.Warnings = append(m.Warnings, "some selected bytes were unavailable")
			}
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		m.Files = files
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

// Import verifies the package first. Dry-run performs no writes. Actual import
// writes only verified payload bytes into a new/empty explicit target and never
// executes package content or applies code.
func Import(pkg, target string, dryRun bool) (Report, error) {
	v, err := inspectPackage(pkg)
	if err != nil {
		return Report{}, err
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
	r := Report{DryRun: dryRun, PackageDir: pkg, Target: canonical, WorkspaceID: v.manifest.WorkspaceID, DeviceID: v.manifest.DeviceID, RecoveryStatus: "incomplete", ArtifactAvailability: v.manifest.ArtifactAvailability, CodeApply: "disabled", AdapterNativeRecovery: "unverified", FilesVerified: len(v.data), BytesVerified: totalBytes(v.data), DoesNotExecute: true, AutoInstall: false, Upload: false}
	r.Warnings = append(r.Warnings, "code apply is disabled; Git commit/dirty worktree restoration is incomplete")
	if dryRun {
		return r, nil
	}
	if _, err := os.Stat(canonical); os.IsNotExist(err) {
		if err := os.MkdirAll(canonical, 0o700); err != nil {
			return Report{}, err
		}
	}
	for rel, data := range v.data {
		dst := filepath.Join(canonical, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return Report{}, err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return Report{}, err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return Report{}, err
		}
		if err := f.Close(); err != nil {
			return Report{}, err
		}
		r.FilesWritten++
		r.Writes++
	}
	rb, _ := json.MarshalIndent(r, "", "  ")
	f, err := os.OpenFile(filepath.Join(canonical, "anchor-import.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Report{}, err
	}
	if _, err := f.Write(append(rb, '\n')); err != nil {
		_ = f.Close()
		return Report{}, err
	}
	if err := f.Close(); err != nil {
		return Report{}, err
	}
	r.Writes++
	return r, nil
}

func totalBytes(m map[string][]byte) int64 {
	var n int64
	for _, b := range m {
		n += int64(len(b))
	}
	return n
}
