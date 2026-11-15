package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sealManifest(t *testing.T, m Manifest) []byte {
	t.Helper()
	m.ContentHash = ""
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	m.ContentHash = hashBytes(b)
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return mb
}

func writeManifest(t *testing.T, pkg string, m Manifest) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), sealManifest(t, m), 0o600); err != nil {
		t.Fatal(err)
	}
}

func baseManifest() Manifest {
	return Manifest{SchemaVersion: SchemaVersion, ExportID: "exp_test", WorkspaceID: "ws", DeviceID: "dev", ArtifactAvailability: "metadata_only", RecoveryStatus: "incomplete"}
}

func TestInspectRejectsUnsafePathsSymlinksExtraAndOversize(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/../evil", SHA256: hashBytes([]byte("x")), Size: 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("traversal package accepted: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		pkg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(pkg, "payload"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(pkg, "payload", "link")); err != nil {
			t.Fatal(err)
		}
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/link", SHA256: hashBytes([]byte("x")), Size: 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink package accepted: %v", err)
		}
	})
	t.Run("extra", func(t *testing.T) {
		pkg := t.TempDir()
		writeManifest(t, pkg, baseManifest())
		if err := os.WriteFile(filepath.Join(pkg, "extra.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "unlisted") {
			t.Fatalf("extra file package accepted: %v", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		pkg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(pkg, "payload"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "payload", "big"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/big", SHA256: hashBytes([]byte("x")), Size: MaxFileBytes + 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "size") {
			t.Fatalf("oversize package accepted: %v", err)
		}
	})
}

func TestImportDryRunWritesNothingAndActualImportDoesNotOverwrite(t *testing.T) {
	pkg := t.TempDir()
	data := []byte("verified fixture\n")
	if err := os.MkdirAll(filepath.Join(pkg, "payload", "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "payload", "evidence", "abc"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := baseManifest()
	m.Files = []FileEntry{{Path: "payload/evidence/abc", SHA256: hashBytes(data), Size: int64(len(data)), Kind: "evidence"}}
	writeManifest(t, pkg, m)
	target := filepath.Join(t.TempDir(), "target")
	r, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.DryRun || r.Writes != 0 || r.RecoveryStatus != "incomplete" || r.CodeApply != "disabled" || !r.DoesNotExecute {
		t.Fatalf("unexpected dry-run report: %+v", r)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry-run created target")
	}
	r, err = Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.FilesWritten != 1 || r.Writes < 1 {
		t.Fatalf("actual import report: %+v", r)
	}
	got, err := os.ReadFile(filepath.Join(target, "payload", "evidence", "abc"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("imported bytes wrong: %v", err)
	}
	if _, err := Import(pkg, target, false); err == nil {
		t.Fatal("second import overwrote non-empty target")
	}
}

// TestImportLegacyPackageMirrorsBytesWithoutRebuilding keeps the pre-event-log
// package format honest: it still imports verified bytes, but reports that no
// workspace was rebuilt instead of pretending recovery happened.
func TestImportLegacyPackageMirrorsBytesWithoutRebuilding(t *testing.T) {
	pkg := t.TempDir()
	data := []byte("legacy payload bytes\n")
	if err := os.MkdirAll(filepath.Join(pkg, "payload", "untracked"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "payload", "untracked", "notes.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := baseManifest()
	m.Files = []FileEntry{{Path: "payload/untracked/notes.txt", SHA256: hashBytes(data), Size: int64(len(data)), Kind: "untracked"}}
	writeManifest(t, pkg, m)

	target := filepath.Join(t.TempDir(), "restored")
	dry, err := Import(pkg, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.WorkspaceRebuilt || dry.Writes != 0 || dry.RecoveryStatus != "incomplete" {
		t.Fatalf("legacy dry run overclaimed: %+v", dry)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry run created the target")
	}
	r, err := Import(pkg, target, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.WorkspaceRebuilt || r.RecoveryStatus != "incomplete" || r.FilesWritten != 1 {
		t.Fatalf("legacy import overclaimed: %+v", r)
	}
	got, err := os.ReadFile(filepath.Join(target, "payload", "untracked", "notes.txt"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("legacy bytes not mirrored: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("legacy import fabricated a workspace manifest")
	}
}
