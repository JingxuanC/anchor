package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const MaxEvidenceBytes = 64 << 20

type EvidenceArtifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

func (s *Store) evidenceRoot() string  { return filepath.Join(s.Dir, "evidence") }
func (s *Store) evidenceBlobs() string { return filepath.Join(s.evidenceRoot(), "blobs") }

func ensureEvidenceDir(root string) error {
	for _, p := range []string{root, filepath.Join(root, "blobs")} {
		info, err := os.Lstat(p)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("evidence directory %s must be a real directory, not a symlink", p)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(p, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("evidence directory %s must be a real directory, not a symlink", p)
		}
	}
	return nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file must be a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	f, err := openEvidenceRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return b, nil
}

// PutEvidenceFile is the explicit artifact ingestion boundary. It rejects
// symlinks, oversize input, recognizable secrets in the filename/metadata/bytes,
// and stores bytes at evidence/blobs/<sha256>. The source file is never scanned
// or collected automatically; the caller must name it explicitly.
func (s *Store) PutEvidenceFile(src, expectedSHA256 string, metadata []string) (EvidenceArtifact, error) {
	if strings.TrimSpace(src) == "" {
		return EvidenceArtifact{}, fmt.Errorf("evidence --file is required")
	}
	if strings.ContainsRune(src, '\x00') {
		return EvidenceArtifact{}, fmt.Errorf("invalid evidence file path")
	}
	if expectedSHA256 != "" {
		expectedSHA256 = strings.ToLower(expectedSHA256)
		if !validSHA256(expectedSHA256) {
			return EvidenceArtifact{}, fmt.Errorf("expected sha256 must be 64 hex characters")
		}
	}
	if err := scanEvidenceMetadata(append([]string{src}, metadata...)...); err != nil {
		return EvidenceArtifact{}, err
	}
	data, err := readRegularFile(src, MaxEvidenceBytes)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	if err := ScanSecrets(data); err != nil {
		return EvidenceArtifact{}, fmt.Errorf("evidence artifact rejected: %w", err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && expectedSHA256 != digest {
		return EvidenceArtifact{}, fmt.Errorf("evidence sha256 mismatch: file is %s", digest)
	}
	artifact := EvidenceArtifact{SHA256: digest, Size: int64(len(data))}

	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireWorkspaceLock(s.Dir)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	defer release()
	if err := ensureEvidenceDir(s.evidenceRoot()); err != nil {
		return EvidenceArtifact{}, err
	}
	dst := filepath.Join(s.evidenceBlobs(), digest)
	if _, err := os.Lstat(dst); err == nil {
		if err := VerifyEvidenceArtifact(s.Dir, EvidenceEntry{SHA256: digest, Size: artifact.Size}); err != nil {
			return EvidenceArtifact{}, fmt.Errorf("existing evidence artifact is invalid: %w", err)
		}
		return artifact, nil
	} else if !os.IsNotExist(err) {
		return EvidenceArtifact{}, err
	}
	f, err := os.CreateTemp(s.evidenceBlobs(), ".evidence-*.tmp")
	if err != nil {
		return EvidenceArtifact{}, err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return EvidenceArtifact{}, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return EvidenceArtifact{}, err
	}
	if err := f.Close(); err != nil {
		return EvidenceArtifact{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return EvidenceArtifact{}, err
	}
	if d, err := os.Open(s.evidenceBlobs()); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return artifact, nil
}

// VerifyEvidenceArtifact re-reads the content-addressed bytes and checks
// existence, regular-file/symlink status, size, digest, metadata, and secret
// material. It never mutates the artifact.
func VerifyEvidenceArtifact(workspace string, entry EvidenceEntry) error {
	digest := strings.ToLower(entry.SHA256)
	if !validSHA256(digest) {
		return fmt.Errorf("artifact sha256 must be 64 hex characters")
	}
	if entry.Size < 0 {
		return fmt.Errorf("artifact size must be non-negative")
	}
	if err := scanEvidenceMetadata(entry.EvidenceID, entry.NodeID, entry.Kind); err != nil {
		return err
	}
	root := filepath.Join(workspace, "evidence")
	for _, p := range []string{root, filepath.Join(root, "blobs")} {
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("evidence artifact store is missing")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("evidence artifact store must be a real directory")
		}
	}
	path := filepath.Join(root, "blobs", digest)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("evidence artifact %s is missing", digest)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("evidence artifact must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("evidence artifact must be a regular file")
	}
	if entry.Size != info.Size() {
		return fmt.Errorf("evidence artifact size mismatch: recorded %d, actual %d", entry.Size, info.Size())
	}
	data, err := readRegularFile(path, MaxEvidenceBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("evidence artifact digest mismatch")
	}
	if err := ScanSecrets(data); err != nil {
		return fmt.Errorf("evidence artifact rejected: %w", err)
	}
	return nil
}

func (s *Store) VerifyEvidence(entry EvidenceEntry) error {
	return VerifyEvidenceArtifact(s.Dir, entry)
}

// LoadReplay verifies artifact bytes as part of replay. CLI status, handoff,
// and every write transaction use this path.
func (s *Store) LoadReplay() (ReplayResult, error) {
	events, err := s.Load()
	if err != nil {
		return ReplayResult{}, err
	}
	return ReplayVerified(events, s.VerifyEvidence), nil
}

type EvidenceAudit struct {
	ReadOnly         bool         `json:"read_only"`
	GCPerformed      bool         `json:"gc_performed"`
	Referenced       []string     `json:"referenced_digests"`
	BlobFiles        []string     `json:"blob_files"`
	OrphanBlobs      []string     `json:"orphan_blobs"`
	InvalidArtifacts []string     `json:"invalid_artifacts"`
	LegacyEvidence   []string     `json:"legacy_evidence"`
	Quarantined      []Quarantine `json:"quarantined,omitempty"`
	MigrationNote    string       `json:"migration_note"`
}

// AuditEvidence is read-only. It never deletes blobs and never rewrites events.
func (s *Store) AuditEvidence() (EvidenceAudit, error) {
	report, err := s.LoadReport()
	if err != nil {
		return EvidenceAudit{}, err
	}
	audit := EvidenceAudit{
		ReadOnly: true, GCPerformed: false, Quarantined: report.Quarantined,
		MigrationNote: "no automatic migration or GC; legacy evidence requires explicit evidence add --file",
	}
	referenced := map[string]bool{}
	for _, e := range report.Events {
		if e.Type != EvEvidenceAdded {
			continue
		}
		var p EvidencePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			audit.InvalidArtifacts = append(audit.InvalidArtifacts, e.EventID+": invalid evidence payload")
			continue
		}
		if p.ArtifactSchema != 1 {
			audit.LegacyEvidence = append(audit.LegacyEvidence, fmt.Sprintf("%s (schema %d)", p.EvidenceID, p.ArtifactSchema))
			continue
		}
		referenced[p.SHA256] = true
		entry := EvidenceEntry{EvidenceID: p.EvidenceID, NodeID: p.NodeID, Kind: p.Kind, SHA256: p.SHA256, Size: p.Size, ArtifactSchema: p.ArtifactSchema}
		if err := s.VerifyEvidence(entry); err != nil {
			audit.InvalidArtifacts = append(audit.InvalidArtifacts, p.EvidenceID+": "+err.Error())
		}
	}
	blobsDir := s.evidenceBlobs()
	if info, err := os.Lstat(blobsDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return EvidenceAudit{}, fmt.Errorf("evidence blob store must be a real directory")
		}
		entries, err := os.ReadDir(blobsDir)
		if err != nil {
			return EvidenceAudit{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			audit.BlobFiles = append(audit.BlobFiles, name)
			if !referenced[name] {
				audit.OrphanBlobs = append(audit.OrphanBlobs, name)
			}
		}
	}
	for digest := range referenced {
		audit.Referenced = append(audit.Referenced, digest)
	}
	sort.Strings(audit.Referenced)
	sort.Strings(audit.BlobFiles)
	sort.Strings(audit.OrphanBlobs)
	sort.Strings(audit.InvalidArtifacts)
	sort.Strings(audit.LegacyEvidence)
	return audit, nil
}

// ReadRegularFileLimited is a shared bounded reader for explicit local inputs.
// It rejects symlinks and non-regular files (including FIFOs/devices) before
// opening, uses O_NOFOLLOW on Unix, and never reads more than limit+1 bytes.
func ReadRegularFileLimited(path string, limit int64) ([]byte, error) {
	return readRegularFile(path, limit)
}
