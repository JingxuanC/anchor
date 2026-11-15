package handoff

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Anchor Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-qm", "initial")
	return repo
}

func TestPrepareCleanRepoIsComplete(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusComplete {
		t.Fatalf("want complete, got %s warnings=%v", p.RecoveryStatus, p.Warnings)
	}
	if p.ArtifactAvailability != "metadata_only" {
		t.Fatalf("unexpected availability %s", p.ArtifactAvailability)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusComplete {
		t.Fatalf("want complete eval, got %+v", ev)
	}
}

func TestDirtyRepoIsIncompleteAndSecretNotInPacket(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "ANCHOR_TEST_SECRET_SHOULD_NOT_APPEAR"
	if err := os.WriteFile(filepath.Join(repo, "untracked.env"), []byte("TOKEN="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.RecoveryStatus != StatusIncomplete {
		t.Fatalf("want incomplete, got %s", p.RecoveryStatus)
	}
	if p.ArtifactAvailability != "metadata_only" {
		t.Fatalf("want metadata_only, got %s", p.ArtifactAvailability)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret content leaked into handoff packet")
	}
	if strings.Contains(string(raw), repo) {
		t.Fatal("absolute repo path leaked into handoff packet")
	}
	var decoded Packet
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range decoded.CodeState.Untracked {
		if f.Path == "untracked.env" && f.SHA256 != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("untracked manifest missing hash")
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusIncomplete {
		t.Fatalf("dirty handoff must evaluate incomplete, got %+v", ev)
	}
}

func TestEvaluateDetectsStaleCodeWithoutOverwriting(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	p, _, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	// Create a new commit after prepare.
	cmd := exec.Command("git", "-C", repo, "commit", "--allow-empty", "-qm", "drift")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ev := Evaluate(dir, repo, &p)
	if ev.Status != StatusStaleCode {
		t.Fatalf("want stale_code, got %+v", ev)
	}
}

func TestNoPacketIsIncompleteAndPathsAreSafe(t *testing.T) {
	ev := Evaluate(t.TempDir(), t.TempDir(), nil)
	if ev.Status != StatusIncomplete {
		t.Fatalf("want incomplete, got %+v", ev)
	}
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../b", "C:/secret", "a:b"} {
		if err := CheckPath(p); err == nil {
			t.Fatalf("unsafe path %q accepted", p)
		}
	}
	if err := CheckPath("src/main.go"); err != nil {
		t.Fatalf("safe path rejected: %v", err)
	}
}

func TestContentHashMismatchRejected(t *testing.T) {
	repo := initRepo(t)
	dir := t.TempDir()
	p, path, err := Prepare(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), `"summary": "`, `"summary": "TAMPERED `, 1))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("tampered packet accepted")
	}
	_ = p
}
