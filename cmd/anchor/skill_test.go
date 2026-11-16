package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillFixture(t *testing.T) (root, proj string) {
	t.Helper()
	root = t.TempDir()
	proj = filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "init", "--attach", "--dir", proj, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	return root, proj
}

func TestCLISkillPrint(t *testing.T) {
	root, _ := skillFixture(t)
	out, stderr, code := runCLI(t, root, "skill", "print")
	if code != 0 {
		t.Fatalf("skill print: %s", stderr)
	}
	for _, want := range []string{"---", "name: anchor", "description:", "# Using Anchor", "handoff prepare", "ANCHOR_CONTEXT_FILE", "anchor_restore_plan"} {
		if !strings.Contains(out, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestCLISkillInstallPlanApplyUninstall(t *testing.T) {
	root, proj := skillFixture(t)
	canonicalProj, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(canonicalProj, ".claude", "skills", "anchor", "SKILL.md")
	out, stderr, code := runCLI(t, root, "skill", "install", "--dir", proj, "--repo", proj, "--json")
	if code != 0 {
		t.Fatalf("plan: %s%s", out, stderr)
	}
	var plan struct {
		Applied bool   `json:"applied"`
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Applied || plan.Path != target {
		t.Fatalf("plan wrong: %s", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("plan wrote the skill file")
	}

	if out, stderr, code := runCLI(t, root, "skill", "install", "--dir", proj, "--repo", proj, "--apply", "--json"); code != 0 {
		t.Fatalf("apply: %s%s", out, stderr)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "name: anchor") {
		t.Fatalf("installed skill wrong: %s", b)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("skill mode wrong: %v %v", info, err)
	}
	statePath := filepath.Join(proj, ".anchor", "adapters", "skill.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("skill state missing: %v", err)
	}
	// Idempotent.
	idem, _, code := runCLI(t, root, "skill", "install", "--dir", proj, "--repo", proj, "--apply", "--json")
	if code != 0 || !strings.Contains(idem, `"already_installed": true`) {
		t.Fatalf("re-install not idempotent: %s", idem)
	}
	// Uninstall removes the file and the recorded state.
	if out, stderr, code := runCLI(t, root, "skill", "install", "--dir", proj, "--repo", proj, "--uninstall", "--apply", "--json"); code != 0 {
		t.Fatalf("uninstall: %s%s", out, stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("skill file not removed")
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("skill state not removed")
	}
}

func TestCLISkillInstallBacksUpAndRespectsEdits(t *testing.T) {
	root, proj := skillFixture(t)
	target := filepath.Join(proj, ".claude", "skills", "anchor", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("user-customized skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLI(t, root, "skill", "install", "--dir", proj, "--repo", proj, "--apply", "--json")
	if code != 0 {
		t.Fatalf("apply: %s%s", out, stderr)
	}
	var plan struct {
		BackupPath string `json:"backup_path"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.BackupPath == "" {
		t.Fatal("existing skill was not backed up")
	}
	if b, err := os.ReadFile(plan.BackupPath); err != nil || string(b) != "user-customized skill\n" {
		t.Fatalf("backup content wrong: %q %v", b, err)
	}
	// Edit after install: uninstall leaves the edited file alone but cleans state.
	if err := os.WriteFile(target, []byte("edited after install\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runCLI(t, root, "skill", "install", "--dir", proj, "--repo", proj, "--uninstall", "--apply", "--json")
	if code != 0 {
		t.Fatalf("uninstall: %s%s", out, stderr)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "edited after install\n" {
		t.Fatalf("edited skill was removed: %q %v", b, err)
	}
	if !strings.Contains(out, "edited") {
		t.Fatalf("drift warning missing: %s", out)
	}
}

func TestCLISkillRequiresRepo(t *testing.T) {
	root, proj := skillFixture(t)
	if _, stderr, code := runCLI(t, root, "skill", "install", "--dir", proj); code != 1 {
		t.Fatalf("missing --repo accepted: %s", stderr)
	}
}
