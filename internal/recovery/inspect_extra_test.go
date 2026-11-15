package recovery

// Extra coverage tests for recovery: package inspection and small primitives.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// packageWithFiles writes the given relative payload files and a sealed
// manifest listing exactly those files.
func packageWithFiles(t *testing.T, files map[string]string) (string, Manifest) {
	t.Helper()
	pkg := t.TempDir()
	m := baseManifest()
	var names []string
	for rel := range files {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		data := []byte(files[rel])
		writeFileR(t, pkg, rel, files[rel])
		m.Files = append(m.Files, FileEntry{Path: rel, SHA256: hashBytes(data), Size: int64(len(data)), Kind: "fixture"})
	}
	writeManifest(t, pkg, m)
	return pkg, m
}

func TestInspectPackageManifestValidation(t *testing.T) {
	t.Run("package must be an existing directory", func(t *testing.T) {
		f := writeFileR(t, t.TempDir(), "file", "x")
		if _, err := inspectPackage(f); err == nil {
			t.Fatal("file accepted as a package")
		}
		if _, err := inspectPackage(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("missing package accepted")
		}
	})

	t.Run("manifest missing", func(t *testing.T) {
		if _, err := inspectPackage(t.TempDir()); err == nil || !strings.Contains(err.Error(), "manifest missing") {
			t.Fatalf("missing manifest: %v", err)
		}
	})

	t.Run("manifest is a directory", func(t *testing.T) {
		pkg := t.TempDir()
		if err := os.Mkdir(filepath.Join(pkg, "manifest.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "manifest missing") {
			t.Fatalf("directory manifest: %v", err)
		}
	})

	t.Run("manifest is a symlink", func(t *testing.T) {
		pkg := t.TempDir()
		target := writeFileR(t, t.TempDir(), "manifest.json", string(sealManifest(t, baseManifest())))
		if err := os.Symlink(target, filepath.Join(pkg, "manifest.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "manifest missing") {
			t.Fatalf("symlink manifest: %v", err)
		}
	})

	t.Run("manifest unreadable", func(t *testing.T) {
		pkg := t.TempDir()
		writeManifest(t, pkg, baseManifest())
		chmodR(t, filepath.Join(pkg, "manifest.json"), 0)
		if _, err := inspectPackage(pkg); err == nil {
			t.Fatal("unreadable manifest accepted")
		}
	})

	t.Run("manifest corrupt json", func(t *testing.T) {
		pkg := t.TempDir()
		writeFileR(t, pkg, "manifest.json", "{not json")
		if _, err := inspectPackage(pkg); err == nil {
			t.Fatal("corrupt manifest accepted")
		}
	})

	t.Run("manifest carries a secret", func(t *testing.T) {
		pkg := t.TempDir()
		writeFileR(t, pkg, "manifest.json", `{"note":"password = hunter2hunter2"}`)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "manifest rejected") {
			t.Fatalf("secret manifest: %v", err)
		}
	})

	t.Run("unsupported schema version", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		m.SchemaVersion = 99
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("schema version: %v", err)
		}
	})

	t.Run("empty export id", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		m.ExportID = ""
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("empty export id: %v", err)
		}
	})

	t.Run("content hash mismatch", func(t *testing.T) {
		pkg := t.TempDir()
		writeManifest(t, pkg, baseManifest())
		raw, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		tampered := strings.Replace(string(raw), `"workspace_id": "ws"`, `"workspace_id": "tampered"`, 1)
		if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(tampered), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
			t.Fatalf("content hash: %v", err)
		}
	})

	t.Run("too many files", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		for i := 0; i <= MaxFiles; i++ {
			m.Files = append(m.Files, FileEntry{Path: "payload/f" + itoa(i), Size: 0})
		}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "too many files") {
			t.Fatalf("too many files: %v", err)
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestInspectPackageFileValidation(t *testing.T) {
	t.Run("duplicate path", func(t *testing.T) {
		pkg, m := packageWithFiles(t, map[string]string{"payload/a": "x"})
		m.Files = append(m.Files, m.Files[0])
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate path: %v", err)
		}
	})

	t.Run("file must be under payload", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		m.Files = []FileEntry{{Path: "top.json", SHA256: hashBytes([]byte("x")), Size: 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "under payload/") {
			t.Fatalf("top-level file: %v", err)
		}
	})

	t.Run("traversal path", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/../../evil", SHA256: hashBytes([]byte("x")), Size: 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("traversal: %v", err)
		}
	})

	t.Run("listed file missing", func(t *testing.T) {
		pkg := t.TempDir()
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/missing", SHA256: hashBytes([]byte("x")), Size: 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil {
			t.Fatal("missing listed file accepted")
		}
	})

	t.Run("listed path is a symlink", func(t *testing.T) {
		pkg := t.TempDir()
		target := writeFileR(t, t.TempDir(), "target", "x")
		writeFileR(t, pkg, "payload/keep", "x")
		if err := os.Symlink(target, filepath.Join(pkg, "payload", "link")); err != nil {
			t.Fatal(err)
		}
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/link", SHA256: hashBytes([]byte("x")), Size: 1}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink payload: %v", err)
		}
	})

	t.Run("listed path is a directory", func(t *testing.T) {
		pkg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(pkg, "payload", "dir"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := baseManifest()
		m.Files = []FileEntry{{Path: "payload/dir", Size: 0}}
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("directory payload: %v", err)
		}
	})

	t.Run("size mismatch", func(t *testing.T) {
		pkg, m := packageWithFiles(t, map[string]string{"payload/a": "abc"})
		m.Files[0].Size = 4
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "size mismatch") {
			t.Fatalf("size mismatch: %v", err)
		}
	})

	t.Run("hash mismatch", func(t *testing.T) {
		pkg, m := packageWithFiles(t, map[string]string{"payload/a": "abc"})
		m.Files[0].SHA256 = hashBytes([]byte("other"))
		writeManifest(t, pkg, m)
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Fatalf("hash mismatch: %v", err)
		}
	})

	t.Run("unreadable payload file", func(t *testing.T) {
		pkg, m := packageWithFiles(t, map[string]string{"payload/a": "abc"})
		_ = m
		chmodR(t, filepath.Join(pkg, "payload", "a"), 0)
		if _, err := inspectPackage(pkg); err == nil {
			t.Fatal("unreadable payload accepted")
		}
	})

	t.Run("secret-bearing payload file", func(t *testing.T) {
		pkg, m := packageWithFiles(t, map[string]string{"payload/a": "password = hunter2hunter2\n"})
		_ = m
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "package file rejected") {
			t.Fatalf("secret payload: %v", err)
		}
	})

	t.Run("unlisted file", func(t *testing.T) {
		pkg := t.TempDir()
		writeManifest(t, pkg, baseManifest())
		writeFileR(t, pkg, "payload/extra", "x")
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "unlisted") {
			t.Fatalf("unlisted file: %v", err)
		}
	})

	t.Run("unlisted symlink", func(t *testing.T) {
		pkg := t.TempDir()
		writeManifest(t, pkg, baseManifest())
		if err := os.MkdirAll(filepath.Join(pkg, "payload"), 0o700); err != nil {
			t.Fatal(err)
		}
		target := writeFileR(t, t.TempDir(), "target", "x")
		if err := os.Symlink(target, filepath.Join(pkg, "payload", "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("unlisted symlink: %v", err)
		}
	})

	t.Run("valid package", func(t *testing.T) {
		pkg, _ := packageWithFiles(t, map[string]string{
			"payload/events/b1.json":     "{\"anchor_batch\":1}",
			"payload/workspace/manifest": "{\"workspace_id\":\"ws\"}",
		})
		v, err := inspectPackage(pkg)
		if err != nil {
			t.Fatal(err)
		}
		if len(v.data) != 2 || len(v.manifest.Files) != 2 {
			t.Fatalf("verified package = %+v", v)
		}
		got := v.data["payload/events/b1.json"]
		if string(got) != "{\"anchor_batch\":1}" {
			t.Fatalf("verified bytes = %q", got)
		}
	})
}

// TestInspectPackageTotalByteLimit crafts sparse payload files whose declared
// sizes sum above MaxTotalBytes; inspection must reject the package.
func TestInspectPackageTotalByteLimit(t *testing.T) {
	const each = int64(52 << 20) // 52 MiB * 5 = 260 MiB > 256 MiB
	pkg := t.TempDir()
	zero := make([]byte, each)
	digest := hashBytes(zero)
	zero = nil // release before inspectPackage re-reads the sparse files

	m := baseManifest()
	if err := os.MkdirAll(filepath.Join(pkg, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		rel := "payload/blob" + itoa(i)
		f, err := os.Create(filepath.Join(pkg, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(each); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, FileEntry{Path: rel, SHA256: digest, Size: each, Kind: "fixture"})
	}
	writeManifest(t, pkg, m)
	if _, err := inspectPackage(pkg); err == nil || !strings.Contains(err.Error(), "total byte limit") {
		t.Fatalf("total byte limit: %v", err)
	}
}

func TestSafePackageBase(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "a:b", "a\x00b"} {
		if err := safePackageBase(name); err == nil {
			t.Fatalf("unsafe package base %q accepted", name)
		}
	}
	for _, name := range []string{"batch_1.json", "abc-DEF_123", "sha256hex"} {
		if err := safePackageBase(name); err != nil {
			t.Fatalf("safe package base %q rejected: %v", name, err)
		}
	}
}

func TestSafePackagePath(t *testing.T) {
	for _, rel := range []string{"/abs", "../x", "payload/../x", "other/x", ""} {
		if err := safePackagePath(rel); err == nil {
			t.Fatalf("unsafe package path %q accepted", rel)
		}
	}
	if err := safePackagePath("payload/events/b1.json"); err != nil {
		t.Fatalf("safe package path rejected: %v", err)
	}
}

func TestWritePackageFile(t *testing.T) {
	t.Run("success records entry", func(t *testing.T) {
		staging := t.TempDir()
		var files []FileEntry
		data := []byte("payload bytes\n")
		if err := writePackageFile(staging, "payload/events/b1.json", "event", data, &files); err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || files[0].Kind != "event" || files[0].Size != int64(len(data)) ||
			files[0].SHA256 != hashBytes(data) {
			t.Fatalf("entry = %+v", files)
		}
		got, err := os.ReadFile(filepath.Join(staging, "payload", "events", "b1.json"))
		if err != nil || string(got) != string(data) {
			t.Fatalf("written bytes = %q %v", got, err)
		}
	})

	t.Run("exclusive create rejects duplicate", func(t *testing.T) {
		staging := t.TempDir()
		var files []FileEntry
		if err := writePackageFile(staging, "payload/x", "fixture", []byte("a"), &files); err != nil {
			t.Fatal(err)
		}
		if err := writePackageFile(staging, "payload/x", "fixture", []byte("b"), &files); err == nil {
			t.Fatal("duplicate payload path written")
		}
	})

	t.Run("secret content rejected", func(t *testing.T) {
		var files []FileEntry
		err := writePackageFile(t.TempDir(), "payload/x", "fixture", []byte("password = hunter2hunter2"), &files)
		if err == nil || !strings.Contains(err.Error(), "package file payload/x rejected") {
			t.Fatalf("secret content: %v", err)
		}
	})

	t.Run("secret path rejected", func(t *testing.T) {
		var files []FileEntry
		err := writePackageFile(t.TempDir(), "payload/password=hunter2hunter2", "fixture", []byte("clean"), &files)
		if err == nil || !strings.Contains(err.Error(), "package path rejected") {
			t.Fatalf("secret path: %v", err)
		}
	})

	t.Run("parent directory blocked by file", func(t *testing.T) {
		staging := t.TempDir()
		writeFileR(t, staging, "payload", "not a directory")
		var files []FileEntry
		if err := writePackageFile(staging, "payload/x", "fixture", []byte("a"), &files); err == nil {
			t.Fatal("writePackageFile wrote through a regular file parent")
		}
	})
}

func TestWriteExclusive(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "nested", "file")
		if err := writeExclusive(dst, []byte("bytes")); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != "bytes" {
			t.Fatalf("bytes = %q %v", got, err)
		}
	})

	t.Run("existing file refused", func(t *testing.T) {
		dst := writeFileR(t, t.TempDir(), "file", "old")
		if err := writeExclusive(dst, []byte("new")); err == nil {
			t.Fatal("writeExclusive overwrote an existing file")
		}
	})

	t.Run("parent is a regular file", func(t *testing.T) {
		parent := writeFileR(t, t.TempDir(), "parent", "x")
		if err := writeExclusive(filepath.Join(parent, "child"), []byte("x")); err == nil {
			t.Fatal("writeExclusive created a child of a regular file")
		}
	})
}

func TestWriteImportReport(t *testing.T) {
	t.Run("success counts its own write", func(t *testing.T) {
		dir := t.TempDir()
		r := Report{FilesWritten: 2, Writes: 2, RecoveryStatus: "complete"}
		got, err := writeImportReport(dir, r)
		if err != nil {
			t.Fatal(err)
		}
		if got.Writes != 3 {
			t.Fatalf("returned writes = %d, want 3", got.Writes)
		}
		b, err := os.ReadFile(filepath.Join(dir, "anchor-import.json"))
		if err != nil {
			t.Fatal(err)
		}
		var decoded Report
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Writes != 3 || decoded.FilesWritten != 2 || decoded.RecoveryStatus != "complete" {
			t.Fatalf("written report = %+v", decoded)
		}
	})

	t.Run("error when canonical path is a file", func(t *testing.T) {
		f := writeFileR(t, t.TempDir(), "file", "x")
		if _, err := writeImportReport(f, Report{}); err == nil {
			t.Fatal("writeImportReport wrote below a regular file")
		}
	})
}

func TestWorkspaceIdentity(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeFileR(t, dir, "manifest.json",
			`{"workspace_id":"ws1","device_id":"dev1","event_log_version":2}`)
		ws, dev, version, err := workspaceIdentity(dir)
		if err != nil || ws != "ws1" || dev != "dev1" || version != 2 {
			t.Fatalf("identity = %q %q %d %v", ws, dev, version, err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		if _, _, _, err := workspaceIdentity(t.TempDir()); !os.IsNotExist(err) {
			t.Fatalf("missing manifest error = %v", err)
		}
	})

	t.Run("corrupt types", func(t *testing.T) {
		dir := t.TempDir()
		writeFileR(t, dir, "manifest.json", `{"workspace_id":123}`)
		if _, _, _, err := workspaceIdentity(dir); err == nil {
			t.Fatal("malformed workspace manifest accepted")
		}
	})
}

func TestRecoveryGitHelper(t *testing.T) {
	notRepo := t.TempDir()
	_, err := git(notRepo, "status")
	if err == nil || !strings.Contains(err.Error(), "git status:") {
		t.Fatalf("git failure = %v", err)
	}
	repo := initGitRepo(t)
	out, err := git(repo, "rev-parse", "HEAD")
	if err != nil || len(strings.TrimSpace(string(out))) != 40 {
		t.Fatalf("git success = %q %v", out, err)
	}
}

func TestCommitImport(t *testing.T) {
	t.Run("existing empty target is replaced", func(t *testing.T) {
		parent := t.TempDir()
		staging := filepath.Join(parent, "staging")
		if err := os.MkdirAll(staging, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFileR(t, staging, "manifest.json", "{}")
		canonical := filepath.Join(parent, "target")
		if err := os.MkdirAll(canonical, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := commitImport(staging, canonical, Report{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(canonical, "manifest.json")); err != nil {
			t.Fatalf("staging not renamed: %v", err)
		}
		if _, err := os.Stat(staging); !os.IsNotExist(err) {
			t.Fatalf("staging directory left behind: %v", err)
		}
	})

	t.Run("non-empty target is refused", func(t *testing.T) {
		staging := t.TempDir()
		canonical := t.TempDir()
		writeFileR(t, canonical, "existing", "x")
		if _, err := commitImport(staging, canonical, Report{}); err == nil {
			t.Fatal("commitImport replaced a non-empty target")
		}
	})

	t.Run("missing target parent is refused", func(t *testing.T) {
		staging := t.TempDir()
		canonical := filepath.Join(t.TempDir(), "missing", "target")
		if _, err := commitImport(staging, canonical, Report{}); err == nil {
			t.Fatal("commitImport created a target without its parent")
		}
	})
}

func TestInspectPackageUnreadableDirectoryFails(t *testing.T) {
	pkg := t.TempDir()
	writeManifest(t, pkg, baseManifest())
	if err := os.MkdirAll(filepath.Join(pkg, "payload", "locked"), 0o700); err != nil {
		t.Fatal(err)
	}
	chmodR(t, filepath.Join(pkg, "payload", "locked"), 0)
	if _, err := inspectPackage(pkg); err == nil {
		t.Fatal("package with an unreadable directory was accepted")
	}
}

func TestWritePackageFileRejectsUnsafeRelAndOversizeData(t *testing.T) {
	t.Run("rel outside payload", func(t *testing.T) {
		var files []FileEntry
		if err := writePackageFile(t.TempDir(), "top.json", "fixture", []byte("x"), &files); err == nil {
			t.Fatal("writePackageFile accepted a path outside payload/")
		}
	})
	t.Run("rel unsafe", func(t *testing.T) {
		var files []FileEntry
		if err := writePackageFile(t.TempDir(), "payload/../escape", "fixture", []byte("x"), &files); err == nil {
			t.Fatal("writePackageFile accepted a traversal path")
		}
	})
	t.Run("oversize data", func(t *testing.T) {
		var files []FileEntry
		data := make([]byte, MaxFileBytes+1)
		if err := writePackageFile(t.TempDir(), "payload/big", "fixture", data, &files); err == nil {
			t.Fatal("writePackageFile accepted oversize data")
		}
	})
}
