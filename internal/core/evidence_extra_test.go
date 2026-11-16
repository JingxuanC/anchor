package core

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestReadRegularFileLimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRegularFileLimited(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	if _, err := ReadRegularFileLimited(path, 4); err == nil {
		t.Fatal("reader accepted content above the limit")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}

	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRegularFileLimited(empty, 0); err != nil || len(got) != 0 {
		t.Fatalf("empty file with zero limit: %q, %v", got, err)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFileLimited(link, 100); err == nil {
		t.Fatal("reader accepted a symlink")
	} else if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := ReadRegularFileLimited(dir, 100); err == nil {
		t.Fatal("reader accepted a directory")
	} else if !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := ReadRegularFileLimited(filepath.Join(dir, "absent"), 100); err == nil {
		t.Fatal("reader accepted a missing file")
	}
	if _, err := ReadRegularFileLimited("", 100); err == nil {
		t.Fatal("reader accepted an empty path")
	}
}

func TestValidSHA256(t *testing.T) {
	valid := []string{
		strings.Repeat("a", 64),
		strings.Repeat("A", 64),
		strings.Repeat("0", 64),
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	for _, s := range valid {
		if !validSHA256(s) {
			t.Errorf("validSHA256(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("g", 64),
		"sha256:" + strings.Repeat("a", 57),
		" " + strings.Repeat("a", 63),
		strings.Repeat("a", 64) + "\n",
	}
	for _, s := range invalid {
		if validSHA256(s) {
			t.Errorf("validSHA256(%q) = true, want false", s)
		}
	}
}

func TestPutEvidenceFileValidation(t *testing.T) {
	ws := t.TempDir()
	s := initAt(t, ws)
	clean := writeArtifact(t, "clean artifact bytes\n")
	cleanDigest := sha256Hex([]byte("clean artifact bytes\n"))
	dirSource := t.TempDir()
	tests := []struct {
		name     string
		src      string
		expect   string
		metadata []string
		want     string
	}{
		{"empty source", "", "", nil, "evidence --file is required"},
		{"whitespace source", "   \t ", "", nil, "evidence --file is required"},
		{"nul in source", "a\x00b", "", nil, "invalid evidence file path"},
		{"invalid expected hash", clean, "not-a-hash", nil, "expected sha256 must be 64 hex"},
		{"mismatched expected hash", clean, strings.Repeat("0", 64), nil, "sha256 mismatch"},
		{"directory source", dirSource, "", nil, "must be a regular file"},
		{"missing source", filepath.Join(t.TempDir(), "absent"), "", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PutEvidenceFile(tc.src, tc.expect, tc.metadata)
			want := tc.want
			if want == "" {
				want = "no such file"
			}
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err, want)
			}
		})
	}
	t.Run("uppercase expected hash matches", func(t *testing.T) {
		art, err := s.PutEvidenceFile(clean, strings.ToUpper(cleanDigest), nil)
		if err != nil {
			t.Fatal(err)
		}
		if art.SHA256 != cleanDigest || art.Size != int64(len("clean artifact bytes\n")) {
			t.Fatalf("unexpected artifact %+v", art)
		}
	})
}

func TestPutEvidenceFileRejectsOversizeSparseSource(t *testing.T) {
	s := initTest(t)
	path := filepath.Join(t.TempDir(), "huge.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxEvidenceBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutEvidenceFile(path, "", nil); err == nil {
		t.Fatal("oversize artifact was accepted")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := ReadRegularFileLimited(path, MaxEvidenceBytes); err == nil {
		t.Fatal("bounded reader accepted oversize input")
	}
}

func TestPutEvidenceFileDeduplicatesAndDetectsCorruptExistingBlob(t *testing.T) {
	ws := t.TempDir()
	s := initAt(t, ws)
	src := writeArtifact(t, "dedup fixture\n")
	first, err := s.PutEvidenceFile(src, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PutEvidenceFile(src, "", nil)
	if err != nil {
		t.Fatalf("second identical put failed: %v", err)
	}
	if second != first {
		t.Fatalf("dedup returned %+v, want %+v", second, first)
	}
	blob := filepath.Join(ws, "evidence", "blobs", first.SHA256)
	if err := os.WriteFile(blob, []byte(strings.Repeat("x", int(first.Size))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutEvidenceFile(src, "", nil); err == nil {
		t.Fatal("corrupt existing blob was accepted")
	} else if !strings.Contains(err.Error(), "existing evidence artifact is invalid") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPutEvidenceFileRejectsBlobsFileInPlaceOfDirectory(t *testing.T) {
	ws := t.TempDir()
	s := initAt(t, ws)
	if err := os.MkdirAll(filepath.Join(ws, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "evidence", "blobs"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := writeArtifact(t, "clean\n")
	if _, err := s.PutEvidenceFile(src, "", nil); err == nil {
		t.Fatal("blobs file in place of directory was accepted")
	} else if !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyEvidenceArtifactBranches(t *testing.T) {
	ws := t.TempDir()
	s := initAt(t, ws)
	src := writeArtifact(t, "verify fixture\n")
	art, err := s.PutEvidenceFile(src, "", []string{"ev1", "n1", "test"})
	if err != nil {
		t.Fatal(err)
	}
	base := EvidenceEntry{EvidenceID: "ev1", NodeID: "n1", Kind: "test", SHA256: art.SHA256, Size: art.Size}
	if err := VerifyEvidenceArtifact(ws, base); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	cases := []struct {
		name      string
		mutate    func(entry *EvidenceEntry)
		workspace string
		want      string
	}{
		{"short sha", func(e *EvidenceEntry) { e.SHA256 = "abcd" }, "", "64 hex"},
		{"non-hex sha", func(e *EvidenceEntry) { e.SHA256 = strings.Repeat("z", 64) }, "", "64 hex"},
		{"negative size", func(e *EvidenceEntry) { e.Size = -1 }, "", "non-negative"},
		{"secret metadata", func(e *EvidenceEntry) { e.Kind = "password = hunter2hunter2" }, "", "evidence metadata rejected"},
		{"missing blob", func(e *EvidenceEntry) { e.SHA256 = strings.Repeat("d", 64) }, "", "is missing"},
		{"wrong size", func(e *EvidenceEntry) { e.Size++ }, "", "size mismatch"},
		{"missing store", nil, t.TempDir(), "store is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := base
			if tc.mutate != nil {
				tc.mutate(&entry)
			}
			workspace := ws
			if tc.workspace != "" {
				workspace = tc.workspace
			}
			err := VerifyEvidenceArtifact(workspace, entry)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	t.Run("evidence root is a file", func(t *testing.T) {
		bad := t.TempDir()
		if err := os.WriteFile(filepath.Join(bad, "evidence"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := VerifyEvidenceArtifact(bad, base); err == nil || !strings.Contains(err.Error(), "must be a real directory") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
	t.Run("blobs is a file", func(t *testing.T) {
		bad := t.TempDir()
		if err := os.Mkdir(filepath.Join(bad, "evidence"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bad, "evidence", "blobs"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := VerifyEvidenceArtifact(bad, base); err == nil || !strings.Contains(err.Error(), "must be a real directory") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
	t.Run("blob is a symlink", func(t *testing.T) {
		digest := strings.Repeat("e", 64)
		outside := writeArtifact(t, "outside\n")
		if err := os.Symlink(outside, filepath.Join(ws, "evidence", "blobs", digest)); err != nil {
			t.Fatal(err)
		}
		entry := EvidenceEntry{EvidenceID: "ev1", NodeID: "n1", Kind: "test", SHA256: digest, Size: int64(len("outside\n"))}
		if err := VerifyEvidenceArtifact(ws, entry); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
	t.Run("blob is a directory", func(t *testing.T) {
		digest := strings.Repeat("f", 64)
		if err := os.Mkdir(filepath.Join(ws, "evidence", "blobs", digest), 0o700); err != nil {
			t.Fatal(err)
		}
		entry := EvidenceEntry{EvidenceID: "ev1", NodeID: "n1", Kind: "test", SHA256: digest, Size: 0}
		if err := VerifyEvidenceArtifact(ws, entry); err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
	t.Run("blob contains a secret", func(t *testing.T) {
		secret := []byte("token=ghp_1234567890abcdefghijklmnopqrstuvwx\n")
		digest := sha256Hex(secret)
		if err := os.WriteFile(filepath.Join(ws, "evidence", "blobs", digest), secret, 0o600); err != nil {
			t.Fatal(err)
		}
		entry := EvidenceEntry{EvidenceID: "ev1", NodeID: "n1", Kind: "test", SHA256: digest, Size: int64(len(secret))}
		if err := VerifyEvidenceArtifact(ws, entry); err == nil || !strings.Contains(err.Error(), "evidence artifact rejected") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
}

func TestAuditEvidenceClassificationAndReadOnly(t *testing.T) {
	ws := t.TempDir()
	s := initAt(t, ws)
	seed(t, s)
	art, err := s.PutEvidenceFile(writeArtifact(t, "audited bytes\n"), "", []string{"ev1", "n1", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Append(Event{Type: EvEvidenceAdded, Payload: evidencePayload("ev1", "n1", art.SHA256, art.Size)}); err != nil {
		t.Fatalf("valid evidence append: %v", err)
	}
	missingDigest := strings.Repeat("c", 64)
	legacyPayload := mustMarshalJSON(t, EvidencePayload{EvidenceID: "legacy-ev", NodeID: "n1", Kind: "test", SHA256: strings.Repeat("a", 64)})
	missingPayload := mustMarshalJSON(t, EvidencePayload{EvidenceID: "missing-ev", NodeID: "n1", Kind: "test", SHA256: missingDigest, Size: 4, ArtifactSchema: 1})
	writeBatchFile(t, filepath.Join(ws, "events"), []Event{
		withID(Event{Type: EvEvidenceAdded, Payload: legacyPayload}, "legacy-ev-event", "d1", 90),
		withID(Event{Type: EvEvidenceAdded, Payload: missingPayload}, "missing-ev-event", "d1", 91),
		withID(Event{Type: EvEvidenceAdded, Payload: []byte(`"not-an-evidence-payload"`)}, "bad-payload-event", "d1", 92),
	})
	orphan := strings.Repeat("b", 64)
	if err := os.WriteFile(filepath.Join(ws, "evidence", "blobs", orphan), []byte("orphan fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, ws)
	audit, err := s.AuditEvidence()
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, ws)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("AuditEvidence mutated the workspace\nbefore=%v\nafter=%v", before, after)
	}
	if !audit.ReadOnly || audit.GCPerformed {
		t.Fatalf("audit must be read-only: %+v", audit)
	}
	if audit.MigrationNote == "" {
		t.Fatal("migration note is empty")
	}
	if len(audit.Quarantined) != 0 {
		t.Fatalf("unexpected quarantine: %+v", audit.Quarantined)
	}
	wantReferenced := []string{art.SHA256, missingDigest}
	sort.Strings(wantReferenced)
	if !reflect.DeepEqual(audit.Referenced, wantReferenced) {
		t.Fatalf("referenced = %v, want %v", audit.Referenced, wantReferenced)
	}
	foundBlob := false
	for _, name := range audit.BlobFiles {
		if name == art.SHA256 {
			foundBlob = true
		}
	}
	if !foundBlob {
		t.Fatalf("stored blob not listed: %v", audit.BlobFiles)
	}
	if !reflect.DeepEqual(audit.OrphanBlobs, []string{orphan}) {
		t.Fatalf("orphan blobs = %v, want [%s]", audit.OrphanBlobs, orphan)
	}
	wantInvalid := []string{"bad-payload-event: invalid evidence payload", "missing-ev: evidence artifact " + missingDigest + " is missing"}
	if !reflect.DeepEqual(audit.InvalidArtifacts, wantInvalid) {
		t.Fatalf("invalid artifacts = %v, want %v", audit.InvalidArtifacts, wantInvalid)
	}
	if !reflect.DeepEqual(audit.LegacyEvidence, []string{"legacy-ev (schema 0)"}) {
		t.Fatalf("legacy evidence = %v", audit.LegacyEvidence)
	}
	// The orphan blob is still on disk: audit never garbage-collects.
	if _, err := os.Stat(filepath.Join(ws, "evidence", "blobs", orphan)); err != nil {
		t.Fatal("audit deleted the orphan blob")
	}
	again, err := s.AuditEvidence()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(audit, again) {
		t.Fatalf("audit is not deterministic:\n%+v\n%+v", audit, again)
	}
}

func TestAuditEvidenceSurfacesQuarantineAndStoreErrors(t *testing.T) {
	t.Run("quarantined events are reported", func(t *testing.T) {
		s := initTest(t)
		writeFileAt(t, filepath.Join(s.Dir, "events", "corrupt.json"), "{not-json")
		audit, err := s.AuditEvidence()
		if err != nil {
			t.Fatal(err)
		}
		if len(audit.Quarantined) != 1 {
			t.Fatalf("quarantine not surfaced: %+v", audit.Quarantined)
		}
	})
	t.Run("missing events directory", func(t *testing.T) {
		if _, err := (&Store{Dir: t.TempDir()}).AuditEvidence(); err == nil {
			t.Fatal("audit accepted a store without an events directory")
		}
	})
	t.Run("blob store symlink", func(t *testing.T) {
		ws := t.TempDir()
		s := initAt(t, ws)
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(ws, "evidence"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(ws, "evidence", "blobs")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuditEvidence(); err == nil {
			t.Fatal("audit followed a symlinked blob store")
		} else if !strings.Contains(err.Error(), "must be a real directory") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("blob store is a file", func(t *testing.T) {
		ws := t.TempDir()
		s := initAt(t, ws)
		if err := os.MkdirAll(filepath.Join(ws, "evidence"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "evidence", "blobs"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuditEvidence(); err == nil {
			t.Fatal("audit accepted a blob file in place of a directory")
		}
	})
	t.Run("missing blob store is not an error", func(t *testing.T) {
		s := initTest(t)
		audit, err := s.AuditEvidence()
		if err != nil {
			t.Fatal(err)
		}
		if len(audit.BlobFiles) != 0 || len(audit.OrphanBlobs) != 0 {
			t.Fatalf("empty workspace reported blobs: %+v", audit)
		}
	})
}

func TestReadRegularFileLimitedRejectsUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of mode")
	}
	path := filepath.Join(t.TempDir(), "unreadable.txt")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()
	if _, err := ReadRegularFileLimited(path, 100); err == nil {
		t.Fatal("unreadable regular file was read")
	}
}

func TestPutEvidenceFileCreateTempFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	ws := t.TempDir()
	s := initAt(t, ws)
	if _, err := s.PutEvidenceFile(writeArtifact(t, "first artifact\n"), "", nil); err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(ws, "evidence", "blobs")
	if err := os.Chmod(blobs, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(blobs, 0o700) }()
	if _, err := s.PutEvidenceFile(writeArtifact(t, "different artifact\n"), "", nil); err == nil {
		t.Fatal("artifact store accepted a write into a read-only blobs directory")
	}
}

func TestLoadReplayPropagatesLoadError(t *testing.T) {
	if _, err := (&Store{Dir: t.TempDir()}).LoadReplay(); err == nil {
		t.Fatal("LoadReplay accepted a store without an event log")
	}
}
