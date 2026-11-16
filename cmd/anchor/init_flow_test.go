package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JingxuanC/anchor/internal/adapter"
)

type initJSON struct {
	StateDir           string   `json:"state_dir"`
	ProjectDir         string   `json:"project_dir"`
	WorkspaceID        string   `json:"workspace_id"`
	DeviceID           string   `json:"device_id"`
	Attached           bool     `json:"attached"`
	AlreadyInitialized bool     `json:"already_initialized"`
	GitIgnored         bool     `json:"git_ignored"`
	Agents             []string `json:"agents"`
	ConfigPath         string   `json:"config_path"`
}

func runInitJSON(t *testing.T, root, proj string, extra ...string) initJSON {
	t.Helper()
	args := append([]string{"init", "--dir", proj, "--json"}, extra...)
	out, stderr, code := runCLI(t, root, args...)
	if code != 0 {
		t.Fatalf("init: %s%s", out, stderr)
	}
	var res initJSON
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("init JSON: %v (%s)", err, out)
	}
	return res
}

func TestCLIInitStreamlinedProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	proj := initRepo(t)

	res := runInitJSON(t, root, proj, "--agents", "claude-code")
	if !res.Attached || res.AlreadyInitialized {
		t.Fatalf("flags wrong: %+v", res)
	}
	if !strings.HasPrefix(res.WorkspaceID, "ws_") || !strings.HasPrefix(res.DeviceID, "dev_") {
		t.Fatalf("ids not generated: %+v", res)
	}
	canonicalProj, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(canonicalProj, ".anchor")
	if res.StateDir != stateDir {
		t.Fatalf("state dir = %q, want %q", res.StateDir, stateDir)
	}
	for _, p := range []string{
		filepath.Join(stateDir, "manifest.json"),
		filepath.Join(proj, ".gitignore"),
		filepath.Join(proj, "CLAUDE.md"),
		filepath.Join(proj, ".mcp.json"),
		filepath.Join(proj, ".claude", "settings.json"),
		filepath.Join(proj, ".claude", "skills", "anchor", "SKILL.md"),
		res.ConfigPath,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s missing: %v", p, err)
		}
	}
	if !res.GitIgnored {
		t.Fatal("expected .anchor/ to be added to .gitignore")
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".gitignore")); !strings.Contains(string(b), ".anchor/") {
		t.Fatalf(".gitignore does not ignore .anchor/: %s", b)
	}
	var settings map[string]any
	b, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.json"))
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "PostToolUse", "SessionEnd"} {
		if _, ok := settings["hooks"].(map[string]any)[event]; !ok {
			t.Fatalf("init did not install %s hook: %v", event, settings)
		}
	}
	var cfg struct {
		Agents []string `json:"agents"`
	}
	b, _ = os.ReadFile(res.ConfigPath)
	if err := json.Unmarshal(b, &cfg); err != nil || strings.Join(cfg.Agents, ",") != "claude-code" {
		t.Fatalf("config agents wrong: %v %v", cfg.Agents, err)
	}
}

func TestCLIInitIdempotentAndStableDevice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	proj := initRepo(t)
	first := runInitJSON(t, root, proj, "--agents", "claude-code")
	second := runInitJSON(t, root, proj, "--agents", "claude-code")
	if !second.AlreadyInitialized {
		t.Fatalf("second init not idempotent: %+v", second)
	}
	if second.WorkspaceID != first.WorkspaceID || second.DeviceID != first.DeviceID {
		t.Fatalf("ids changed on re-init: %+v vs %+v", first, second)
	}
	if second.GitIgnored {
		t.Fatal("re-init modified .gitignore again")
	}
	// A second project on the same machine shares the device id.
	other := initRepo(t)
	third := runInitJSON(t, root, other, "--agents", "none")
	if third.DeviceID != first.DeviceID {
		t.Fatalf("device id not stable across projects: %q vs %q", first.DeviceID, third.DeviceID)
	}
	if third.WorkspaceID == first.WorkspaceID {
		t.Fatal("different projects got the same workspace id")
	}
	// Re-init with another agent unions the config without re-rendering files.
	fourth := runInitJSON(t, root, proj, "--agents", "codex")
	var cfg struct {
		Agents []string `json:"agents"`
	}
	b, _ := os.ReadFile(fourth.ConfigPath)
	_ = json.Unmarshal(b, &cfg)
	if strings.Join(cfg.Agents, ",") != "claude-code,codex" {
		t.Fatalf("re-init did not merge agents: %v", cfg.Agents)
	}
}

func TestCLIInitNoAgentSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	proj := initRepo(t)
	res := runInitJSON(t, root, proj, "--no-agent-setup")
	if len(res.Agents) != 0 || res.ConfigPath != "" {
		t.Fatalf("agent setup ran despite --no-agent-setup: %+v", res)
	}
	for _, p := range []string{"CLAUDE.md", ".mcp.json", filepath.Join(".claude", "settings.json")} {
		if _, err := os.Stat(filepath.Join(proj, p)); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist with --no-agent-setup", p)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, ".anchor", "manifest.json")); err != nil {
		t.Fatalf("state missing: %v", err)
	}
}

func TestCLIInitNonGitDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	proj := filepath.Join(root, "plain")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	res := runInitJSON(t, root, proj, "--agents", "none")
	if res.GitIgnored {
		t.Fatal("non-git directory reported a gitignore change")
	}
	if _, err := os.Stat(filepath.Join(proj, ".gitignore")); !os.IsNotExist(err) {
		t.Fatal(".gitignore was created in a non-git directory")
	}
	if _, err := os.Stat(res.StateDir); err != nil {
		t.Fatalf("state dir missing: %v", err)
	}
}

func TestCLIInitLegacyExplicitIDsStillWork(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	// Legacy mode intentionally keeps its text output; assert the ids are honored.
	out, stderr, code := runCLI(t, root, "init", "--dir", ws, "--workspace", "w1", "--device", "d1")
	if code != 0 {
		t.Fatalf("legacy init: %s%s", out, stderr)
	}
	if !strings.Contains(out, "workspace w1") || !strings.Contains(out, "device d1") {
		t.Fatalf("legacy init output wrong: %s", out)
	}
	b, err := os.ReadFile(filepath.Join(ws, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil || m.WorkspaceID != "w1" || m.DeviceID != "d1" {
		t.Fatalf("manifest ids wrong: %s", b)
	}
	if _, err := os.Stat(filepath.Join(ws, ".anchor")); !os.IsNotExist(err) {
		t.Fatal("legacy state dir unexpectedly nested .anchor")
	}
}

func TestCLIInitRejectsUnknownAgent(t *testing.T) {
	root := t.TempDir()
	proj := initRepo(t)
	if _, stderr, code := runCLI(t, root, "init", "--dir", proj, "--agents", "nope"); code != 1 {
		t.Fatalf("unknown agent accepted: %s", stderr)
	}
}

func TestResolveInitAgents(t *testing.T) {
	dets := []adapterDetection{
		{AdapterID: "claude-code", Installed: true},
		{AdapterID: "codex", Installed: false},
		{AdapterID: "gemini-cli", Installed: true},
	}
	got, err := resolveInitAgents("auto", toDetections(dets))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "claude-code,gemini-cli" {
		t.Fatalf("auto selection = %v", got)
	}
	if got, err := resolveInitAgents("none", nil); err != nil || len(got) != 0 {
		t.Fatalf("none = %v %v", got, err)
	}
	if got, err := resolveInitAgents("codex", nil); err != nil || strings.Join(got, ",") != "codex" {
		t.Fatalf("explicit = %v %v", got, err)
	}
	if _, err := resolveInitAgents("unknown", nil); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

// adapterDetection is a tiny test mirror of adapter.Detection.
type adapterDetection struct {
	AdapterID string
	Installed bool
}

func toDetections(in []adapterDetection) []adapter.Detection {
	out := make([]adapter.Detection, 0, len(in))
	for _, d := range in {
		out = append(out, adapter.Detection{AdapterID: d.AdapterID, Installed: d.Installed})
	}
	return out
}

func TestCLIVersionAndInstallScript(t *testing.T) {
	root := t.TempDir()
	out, stderr, code := runCLI(t, root, "version")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "anchor ") {
		t.Fatalf("version: %s%s", out, stderr)
	}
	// install.sh must stay POSIX-sh parseable.
	script := filepath.Join("..", "..", "install.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("install.sh not present: %v", err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(sh, "-n", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install.sh syntax: %v %s", err, out)
	}
}
