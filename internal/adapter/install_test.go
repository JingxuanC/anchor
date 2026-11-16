package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installFixture(t *testing.T) (stateDir, repoDir, anchorPath string) {
	t.Helper()
	stateDir = t.TempDir()
	repoDir = t.TempDir()
	anchorPath = filepath.Join(t.TempDir(), "anchor-bin")
	if err := os.WriteFile(anchorPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return
}

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func managedCount(settings map[string]any, anchorPath string) int {
	count := 0
	hooks, _ := settings["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "PostToolUse", "SessionEnd"} {
		groups, _ := hooks[event].([]any)
		for _, g := range groups {
			group, _ := g.(map[string]any)
			list, _ := group["hooks"].([]any)
			for _, h := range list {
				hook, _ := h.(map[string]any)
				if cmd, _ := hook["command"].(string); strings.Contains(cmd, anchorPath) {
					count++
				}
			}
		}
	}
	return count
}

func TestInstallPlanThenApplyThenUninstall(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	opts := InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Now: func() time.Time { return time.Unix(1000, 0) }}

	plan, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || len(plan.Operations) == 0 {
		t.Fatalf("dry run should plan without applying: %+v", plan)
	}
	if _, err := os.Stat(plan.SettingsPath); !os.IsNotExist(err) {
		t.Fatal("dry run wrote settings.json")
	}
	if plan.BackupPath != "" {
		t.Fatalf("no backup expected for a new file: %q", plan.BackupPath)
	}

	opts.Apply = true
	applied, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied {
		t.Fatalf("apply did not report applied: %+v", applied)
	}
	settings := readSettingsFile(t, applied.SettingsPath)
	if got := managedCount(settings, anchorPath); got != 3 {
		t.Fatalf("expected 2 managed hooks, got %d: %s", got, settings)
	}
	if _, err := os.Stat(applied.StatePath); err != nil {
		t.Fatalf("install state missing: %v", err)
	}

	// Idempotent.
	again, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyInstalled || again.Applied {
		t.Fatalf("second install should be a no-op: %+v", again)
	}

	// Uninstall removes exactly the managed entries and the state file.
	opts.Uninstall = true
	un, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !un.Applied || un.Removed != 3 {
		t.Fatalf("uninstall wrong: %+v", un)
	}
	settings = readSettingsFile(t, un.SettingsPath)
	if got := managedCount(settings, anchorPath); got != 0 {
		t.Fatalf("managed hooks left behind: %s", settings)
	}
	if _, err := os.Stat(un.StatePath); !os.IsNotExist(err) {
		t.Fatal("install state was not removed")
	}
	if un.BackupPath == "" {
		t.Fatal("uninstall did not back up the settings file")
	}
}

func TestInstallPreservesUserSettingsAndBacksUp(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	dir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	user := `{
  "permissions": {"allow": ["Bash(git status)"]},
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "user-command"}]}
    ]
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true, Now: func() time.Time { return time.Unix(2000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if plan.BackupPath == "" {
		t.Fatal("existing settings were not backed up")
	}
	if _, err := os.Stat(plan.BackupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	settings := readSettingsFile(t, plan.SettingsPath)
	if _, ok := settings["permissions"]; !ok {
		t.Fatalf("user permissions key was lost: %s", settings)
	}
	groups, _ := settings["hooks"].(map[string]any)["PostToolUse"].([]any)
	foundUser := false
	for _, g := range groups {
		group, _ := g.(map[string]any)
		list, _ := group["hooks"].([]any)
		for _, h := range list {
			hook, _ := h.(map[string]any)
			if hook["command"] == "user-command" {
				foundUser = true
			}
		}
	}
	if !foundUser {
		t.Fatalf("user hook was lost: %s", settings)
	}
	if got := managedCount(settings, anchorPath); got != 3 {
		t.Fatalf("managed hooks missing: %d", got)
	}
}

func TestInstallRefusesUnsafeTargets(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	// Symlinked settings.json.
	dir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "settings.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlinked settings accepted: %v", err)
	}
	// Unparsable settings are never clobbered.
	if err := os.Remove(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true}); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Fatalf("unparsable settings were modified: %v", err)
	}
	// Unsupported adapter / scope / missing repo.
	if _, err := Install(InstallOptions{AdapterID: "codex", StateDir: stateDir, RepoDir: repoDir}); err == nil {
		t.Fatal("codex hook installation accepted")
	}
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir, Scope: "galaxy"}); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, Scope: ScopeProject}); err == nil {
		t.Fatal("project scope without repo accepted")
	}
}

func TestInstallUserScopeAndQuoting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := t.TempDir()
	anchorDir := filepath.Join(t.TempDir(), "with space")
	if err := os.MkdirAll(anchorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	anchorPath := filepath.Join(anchorDir, "anchor bin")
	if err := os.WriteFile(anchorPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, Scope: ScopeUser,
		AnchorPath: anchorPath, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".claude", "settings.json")
	if plan.SettingsPath != want {
		t.Fatalf("user scope settings path = %q, want %q", plan.SettingsPath, want)
	}
	settings := readSettingsFile(t, plan.SettingsPath)
	hooks := settings["hooks"].(map[string]any)
	group := hooks["PostToolUse"].([]any)[0].(map[string]any)
	hook := group["hooks"].([]any)[0].(map[string]any)
	command, _ := hook["command"].(string)
	if !strings.Contains(command, "anchor bin'") || !strings.HasPrefix(command, "'/") {
		t.Fatalf("anchor path with spaces was not single-quoted: %q", command)
	}
	if !strings.Contains(command, "--capture-dir") {
		t.Fatalf("hook command does not capture: %q", command)
	}
}

func TestLoadInstallStateMissingIsNil(t *testing.T) {
	state, err := LoadInstallState(t.TempDir(), "claude-code")
	if err != nil || state != nil {
		t.Fatalf("missing state = %+v, %v", state, err)
	}
}

func TestUninstallEdgeCases(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	opts := InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true}
	// No recorded installation.
	opts.Uninstall = true
	plan, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || len(plan.Warnings) == 0 {
		t.Fatalf("uninstall without state should warn, not apply: %+v", plan)
	}
	// Install, then remove the settings file: uninstall must not fabricate it.
	opts.Uninstall = false
	installed, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(installed.SettingsPath); err != nil {
		t.Fatal(err)
	}
	opts.Uninstall = true
	plan, err = Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied || len(plan.Warnings) == 0 {
		t.Fatalf("uninstall with missing settings: %+v", plan)
	}
	if _, err := os.Stat(plan.StatePath); !os.IsNotExist(err) {
		t.Fatal("state not removed when settings were missing")
	}
}

func TestUninstallAfterUserEditsIsDriftTolerant(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	opts := InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true}
	installed, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the managed entries by hand (simulating a user edit).
	if err := os.WriteFile(installed.SettingsPath, []byte("{\"hooks\": {}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Uninstall = true
	plan, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied || plan.Removed != 0 || len(plan.Warnings) == 0 {
		t.Fatalf("drifted uninstall should clean state with a warning: %+v", plan)
	}
	if _, err := os.Stat(plan.StatePath); !os.IsNotExist(err) {
		t.Fatal("drifted uninstall left the recorded state behind")
	}
}

func TestMalformedHookStructuresAreReplaced(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	dir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// hooks is an object but the event value is malformed.
	raw := `{"hooks": {"PostToolUse": "not-a-list"}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	settings := readSettingsFile(t, plan.SettingsPath)
	groups, ok := settings["hooks"].(map[string]any)["PostToolUse"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("malformed event value was not replaced: %s", settings)
	}
}

func TestInstallStateCorruptionIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "adapters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "adapters", "claude-code.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstallState(dir, "claude-code"); err == nil || !strings.Contains(err.Error(), "invalid adapter install state") {
		t.Fatalf("corrupt state not reported: %v", err)
	}
}

func TestInstallRefusesNonObjectHooksValue(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	dir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"hooks": "not-an-object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true}); err == nil || !strings.Contains(err.Error(), "hooks value is not an object") {
		t.Fatalf("non-object hooks value was accepted: %v", err)
	}
}

func TestDetectUnparsableVersionStillInstalled(t *testing.T) {
	pathDir := t.TempDir()
	fakeBinary(t, pathDir, "claude", "echo 'Claude Code (nightly)'\n")
	t.Setenv("PATH", pathDir)
	d := Detect("claude-code", 5*time.Second)
	if !d.Installed || d.Version != "" || !strings.Contains(d.Error, "could not be parsed") {
		t.Fatalf("unparsable version detection wrong: %+v", d)
	}
}

func TestInstallWithMCPRegistration(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	// A user's existing MCP config must be preserved.
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), []byte(`{"mcpServers": {"other": {"command": "other-server", "args": []}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, WithMCP: true, Apply: true,
		Now: func() time.Time { return time.Unix(3000, 0) }}
	plan, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPPath == "" || !plan.MCPConfigured || plan.MCPBackupPath == "" {
		t.Fatalf("mcp registration not applied: %+v", plan)
	}
	raw, err := os.ReadFile(plan.MCPPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	servers := cfg["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("user MCP server lost: %s", raw)
	}
	anchor, ok := servers["anchor"].(map[string]any)
	if !ok {
		t.Fatalf("anchor MCP server missing: %s", raw)
	}
	args, _ := anchor["args"].([]any)
	if len(args) != 4 || args[0] != "mcp" || args[1] != "serve" || args[2] != "--dir" {
		t.Fatalf("anchor MCP args wrong: %v", anchor)
	}
	if anchor["command"] != plan.AnchorPath {
		t.Fatalf("anchor MCP command = %v, want %s", anchor["command"], plan.AnchorPath)
	}
	// Idempotent.
	again, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyInstalled {
		t.Fatalf("MCP re-install not idempotent: %+v", again)
	}
	// Uninstall removes both hook entries and the anchor MCP server.
	opts.Uninstall = true
	plan, err = Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.MCPRemoved || !plan.Applied {
		t.Fatalf("MCP not removed: %+v", plan)
	}
	raw, err = os.ReadFile(plan.MCPPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	servers = cfg["mcpServers"].(map[string]any)
	if _, ok := servers["anchor"]; ok {
		t.Fatalf("anchor MCP server left behind: %s", raw)
	}
	if _, ok := servers["other"]; !ok {
		t.Fatalf("user MCP server lost on uninstall: %s", raw)
	}
}

func TestInstallWithMCPRefusesBadConfigAndUserScope(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeUser, AnchorPath: anchorPath, WithMCP: true, Apply: true}); err == nil || !strings.Contains(err.Error(), "requires project scope") {
		t.Fatalf("user-scope MCP accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".mcp.json"), []byte(`{"mcpServers": "bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, WithMCP: true, Apply: true}); err == nil || !strings.Contains(err.Error(), "mcpServers value is not an object") {
		t.Fatalf("malformed MCP config accepted: %v", err)
	}
}

func TestInstallSessionContextHookToggle(t *testing.T) {
	stateDir, repoDir, anchorPath := installFixture(t)
	opts := InstallOptions{AdapterID: "claude-code", StateDir: stateDir, RepoDir: repoDir,
		Scope: ScopeProject, AnchorPath: anchorPath, Apply: true}
	plan, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AlreadyInstalled {
		t.Fatalf("first install should change something: %+v", plan)
	}
	settings := readSettingsFile(t, plan.SettingsPath)
	if got := managedCount(settings, anchorPath); got != 3 {
		t.Fatalf("expected 3 managed hooks (SessionStart/PostToolUse/SessionEnd), got %d", got)
	}
	state, err := LoadInstallState(stateDir, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]bool{}
	for _, e := range state.Entries {
		events[e.Event] = true
	}
	if !events["SessionStart"] {
		t.Fatalf("SessionStart missing from install state: %+v", state.Entries)
	}
	// Disabling session context removes only SessionStart.
	opts.NoSessionContext = true
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	settings = readSettingsFile(t, plan.SettingsPath)
	if got := managedCount(settings, anchorPath); got != 2 {
		t.Fatalf("SessionStart not removed: %d managed hooks left", got)
	}
	if _, ok := settings["hooks"].(map[string]any)["SessionStart"]; ok {
		t.Fatalf("SessionStart event key left behind: %s", settings)
	}
	state, _ = LoadInstallState(stateDir, "claude-code")
	for _, e := range state.Entries {
		if e.Event == "SessionStart" {
			t.Fatal("state still records SessionStart after disabling it")
		}
	}
	// Re-enabling restores it.
	opts.NoSessionContext = false
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	settings = readSettingsFile(t, plan.SettingsPath)
	if got := managedCount(settings, anchorPath); got != 3 {
		t.Fatalf("SessionStart not restored: %d managed hooks", got)
	}
}
