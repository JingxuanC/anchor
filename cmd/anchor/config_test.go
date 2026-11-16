package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configFixture(t *testing.T) (root, proj string) {
	root, proj = treeFixture(t)
	return root, proj
}

func TestCLIConfigInitPlanAndApply(t *testing.T) {
	root, proj := configFixture(t)
	out, stderr, code := runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--agents", "claude-code,codex", "--json")
	if code != 0 {
		t.Fatalf("init plan: %s%s", out, stderr)
	}
	var plan struct {
		Path    string `json:"path"`
		Applied bool   `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Applied {
		t.Fatal("init plan applied the config")
	}
	if _, err := os.Stat(plan.Path); !os.IsNotExist(err) {
		t.Fatal("init plan wrote the config file")
	}
	canonicalProj, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(canonicalProj, ".anchor")
	cfgPath := filepath.Join(stateDir, "config", "config.json")
	if plan.Path != cfgPath {
		t.Fatalf("config path = %q, want %q", plan.Path, cfgPath)
	}
	if out, stderr, code := runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--agents", "claude-code,codex", "--apply"); code != 0 {
		t.Fatalf("init apply: %s%s", out, stderr)
	}
	show, _, code := runCLI(t, root, "config", "show", "--dir", proj, "--json")
	if code != 0 {
		t.Fatal("show failed")
	}
	var shown struct {
		Config struct {
			Agents []string `json:"agents"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(show), &shown); err != nil {
		t.Fatal(err)
	}
	if strings.Join(shown.Config.Agents, ",") != "claude-code,codex" {
		t.Fatalf("agents = %v", shown.Config.Agents)
	}
}

func TestCLIConfigApplyRendersManagedBlocks(t *testing.T) {
	root, proj := configFixture(t)
	runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--agents", "claude-code,codex", "--apply")

	// User content outside the block must survive apply.
	userBefore := "# My project notes\n\nkeep me\n"
	if err := os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte(userBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply", "--json")
	if code != 0 {
		t.Fatalf("apply: %s%s", out, stderr)
	}
	claude, err := os.ReadFile(filepath.Join(proj, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claude), "keep me") || !strings.Contains(string(claude), "## Anchor workspace rules (agent: claude-code)") {
		t.Fatalf("CLAUDE.md merge wrong:\n%s", claude)
	}
	if !strings.Contains(string(claude), "anchor:begin") || !strings.Contains(string(claude), "anchor:end") {
		t.Fatal("managed markers missing")
	}
	// The rules block must instruct Anchor usage at start/completion/branch.
	for _, want := range []string{"**Start.**", "**Completion.**", "decision node", "anchor_restore_plan"} {
		if !strings.Contains(string(claude), want) {
			t.Fatalf("rules block missing %q", want)
		}
	}
	// Codex gets AGENTS.md and the snippet artifact; Claude gets .mcp.json + skill.
	for _, p := range []string{"AGENTS.md", ".mcp.json", filepath.Join(".claude", "skills", "anchor", "SKILL.md")} {
		if _, err := os.Stat(filepath.Join(proj, p)); err != nil {
			t.Fatalf("%s missing: %v", p, err)
		}
	}
	snippet := filepath.Join(proj, ".anchor", "config", "rendered", "codex-config.toml")
	if b, err := os.ReadFile(snippet); err != nil || !strings.Contains(string(b), "[mcp_servers.anchor]") {
		t.Fatalf("codex snippet wrong: %v %s", err, b)
	}
	mcpRaw, err := os.ReadFile(filepath.Join(proj, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mcp map[string]any
	if err := json.Unmarshal(mcpRaw, &mcp); err != nil {
		t.Fatal(err)
	}
	servers := mcp["mcpServers"].(map[string]any)
	if _, ok := servers["anchor"]; !ok {
		t.Fatalf(".mcp.json missing anchor: %s", mcpRaw)
	}

	// Idempotent: a second apply reports unchanged operations and keeps user text.
	again, _, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply", "--json")
	if code != 0 {
		t.Fatal("second apply failed")
	}
	if !strings.Contains(again, `"unchanged"`) {
		t.Fatalf("second apply should be unchanged: %s", again)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, "CLAUDE.md")); strings.Count(string(b), "keep me") != 1 {
		t.Fatal("user content duplicated or lost on re-apply")
	}
}

func TestCLIConfigStatusDetectsDriftAndFixesIt(t *testing.T) {
	root, proj := configFixture(t)
	runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--apply")
	runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply")

	statusOut, _, code := runCLI(t, root, "config", "status", "--dir", proj, "--repo", proj, "--json")
	if code != 0 {
		t.Fatal("status failed")
	}
	var clean struct {
		Clean bool `json:"clean"`
	}
	if err := json.Unmarshal([]byte(statusOut), &clean); err != nil || !clean.Clean {
		t.Fatalf("freshly applied config should be clean: %s", statusOut)
	}

	// Edit inside the managed block -> drift; --diff shows the difference.
	path := filepath.Join(proj, "CLAUDE.md")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), "**Start.**", "**Start.** user edit", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	statusOut, _, _ = runCLI(t, root, "config", "status", "--dir", proj, "--repo", proj, "--json")
	if err := json.Unmarshal([]byte(statusOut), &clean); err != nil || clean.Clean {
		t.Fatalf("drift not detected: %s", statusOut)
	}
	diff, _, code := runCLI(t, root, "config", "plan", "--dir", proj, "--repo", proj, "--diff")
	if code != 0 || !strings.Contains(diff, "desired managed block") || !strings.Contains(diff, "user edit") {
		t.Fatalf("diff did not show drift: %s", diff)
	}
	// Applying again repairs the block, preserving user content outside it.
	if err := os.WriteFile(path, append([]byte("# user intro\n"), []byte("\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("repair apply: %s%s", out, stderr)
	}
	statusOut, _, _ = runCLI(t, root, "config", "status", "--dir", proj, "--repo", proj, "--json")
	if err := json.Unmarshal([]byte(statusOut), &clean); err != nil || !clean.Clean {
		t.Fatalf("apply did not repair drift: %s", statusOut)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "# user intro") {
		t.Fatal("user content lost during repair")
	}
}

func TestCLIConfigRollbackRestoresSnapshot(t *testing.T) {
	root, proj := configFixture(t)
	runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--apply")
	runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply")
	path := filepath.Join(proj, "CLAUDE.md")
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "## Anchor workspace rules") {
		t.Fatalf("first render missing: %v", err)
	}
	// Change the config text, apply v2, then roll back to the v1 snapshot.
	cfgPath := filepath.Join(proj, ".anchor", "config", "config.json")
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	cfg["rules"] = map[string]any{"start": "V2 START INSTRUCTION"}
	updated, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply")
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "V2 START INSTRUCTION") {
		t.Fatal("v2 not applied")
	}
	if out, stderr, code := runCLI(t, root, "config", "rollback", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("rollback: %s%s", out, stderr)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "V2 START INSTRUCTION") || !strings.Contains(string(b), "## Anchor workspace rules") {
		t.Fatalf("rollback did not restore the preview snapshot:\n%s", b)
	}
}

func TestCLIConfigSecretRefsOnly(t *testing.T) {
	root, proj := configFixture(t)
	runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--apply")
	cfgPath := filepath.Join(proj, ".anchor", "config", "config.json")
	var cfg map[string]any
	b, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(b, &cfg)
	cfg["secret_refs"] = map[string]any{"API_KEY": "env:MY_API_KEY", "BAD": "sk-literal-secret"}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code == 0 {
		t.Fatalf("literal secret accepted: %s", stderr)
	}
	// References are accepted and never rendered into files.
	cfg["secret_refs"] = map[string]any{"API_KEY": "env:MY_API_KEY"}
	raw, _ = json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, raw, 0o600)
	if out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("refs rejected: %s%s", out, stderr)
	}
	for _, p := range []string{"CLAUDE.md", "AGENTS.md", ".mcp.json", filepath.Join(".claude", "skills", "anchor", "SKILL.md")} {
		if data, err := os.ReadFile(filepath.Join(proj, p)); err == nil && strings.Contains(string(data), "MY_API_KEY") {
			t.Fatalf("secret reference rendered into %s", p)
		}
	}
}

func TestCLIConfigScopePrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, proj := configFixture(t)
	// User scope enables only claude-code; project scope overrides with codex too.
	if out, stderr, code := runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--scope", "user", "--agents", "claude-code", "--apply"); code != 0 {
		t.Fatalf("user init: %s%s", out, stderr)
	}
	if out, stderr, code := runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--scope", "project", "--agents", "claude-code,codex", "--apply"); code != 0 {
		t.Fatalf("project init: %s%s", out, stderr)
	}
	show, _, code := runCLI(t, root, "config", "show", "--dir", proj, "--json")
	if code != 0 {
		t.Fatal("show failed")
	}
	var shown struct {
		Config struct {
			Agents []string `json:"agents"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(show), &shown); err != nil {
		t.Fatal(err)
	}
	if strings.Join(shown.Config.Agents, ",") != "claude-code,codex" {
		t.Fatalf("project scope did not override user scope: %v", shown.Config.Agents)
	}
}

func TestCLIConfigOwnsHooks(t *testing.T) {
	root, proj := configFixture(t)
	runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--agents", "claude-code", "--apply")
	if out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("apply: %s%s", out, stderr)
	}
	settingsPath := filepath.Join(proj, ".claude", "settings.json")
	var settings map[string]any
	readJSONFile(t, settingsPath, &settings)
	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "PostToolUse", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Fatalf("config apply did not install %s hook: %s", event, jsonString(t, settings))
		}
	}
	// The installed SessionStart hook injects context.
	raw := jsonString(t, hooks["SessionStart"])
	if !strings.Contains(raw, "adapter context") || !strings.Contains(raw, "adapter context --dir") {
		t.Fatalf("SessionStart hook is not a context hook: %s", raw)
	}
	// Status is clean with hooks present.
	statusOut, _, _ := runCLI(t, root, "config", "status", "--dir", proj, "--repo", proj, "--json")
	var st struct {
		Clean bool `json:"clean"`
	}
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil || !st.Clean {
		t.Fatalf("config status not clean after apply: %s", statusOut)
	}

	// Disable session context: only that hook is removed.
	editConfig(t, proj, func(cfg map[string]any) {
		h := cfg["hooks"].(map[string]any)
		h["session_start"] = false
	})
	if out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("disable session apply: %s%s", out, stderr)
	}
	readJSONFile(t, settingsPath, &settings)
	hooks = settings["hooks"].(map[string]any)
	if _, ok := hooks["SessionStart"]; ok {
		t.Fatalf("SessionStart hook was not removed: %s", jsonString(t, hooks))
	}
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Fatal("PostToolUse hook removed unexpectedly")
	}

	// Disable all hooks: every managed hook is removed.
	editConfig(t, proj, func(cfg map[string]any) {
		h := cfg["hooks"].(map[string]any)
		h["claude_code"] = false
	})
	if out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("disable hooks apply: %s%s", out, stderr)
	}
	readJSONFile(t, settingsPath, &settings)
	hooks, _ = settings["hooks"].(map[string]any)
	if len(hooks) != 0 {
		t.Fatalf("managed hooks left after disabling them: %s", jsonString(t, hooks))
	}
	statusOut, _, _ = runCLI(t, root, "config", "status", "--dir", proj, "--repo", proj, "--json")
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil || !st.Clean {
		t.Fatalf("config status not clean with hooks disabled: %s", statusOut)
	}
}

func TestCLIConfigRendersSkillPlaybookForCodexAndGemini(t *testing.T) {
	root, proj := configFixture(t)
	runCLI(t, root, "config", "init", "--dir", proj, "--repo", proj, "--agents", "claude-code,codex,gemini-cli", "--apply")
	if out, stderr, code := runCLI(t, root, "config", "apply", "--dir", proj, "--repo", proj, "--apply"); code != 0 {
		t.Fatalf("apply: %s%s", out, stderr)
	}
	for _, file := range []string{"AGENTS.md", "GEMINI.md"} {
		b, err := os.ReadFile(filepath.Join(proj, file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		text := string(b)
		if !strings.Contains(text, "Agent skill (activated through this rules file)") {
			t.Fatalf("%s missing the adapter-specific skill playbook", file)
		}
		for _, want := range []string{"Start:", "Work:", "Finish:", "Resume:"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s skill playbook missing %q", file, want)
			}
		}
	}
	// Claude gets the dedicated skill file and a pointer in its rules block.
	claude, _ := os.ReadFile(filepath.Join(proj, "CLAUDE.md"))
	if !strings.Contains(string(claude), ".claude/skills/anchor/SKILL.md") {
		t.Fatal("CLAUDE.md does not reference the installed skill")
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude", "skills", "anchor", "SKILL.md")); err != nil {
		t.Fatalf("Claude skill not installed: %v", err)
	}
}

func readJSONFile(t *testing.T, path string, out any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func editConfig(t *testing.T, proj string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(proj, ".anchor", "config", "config.json")
	var cfg map[string]any
	readJSONFile(t, path, &cfg)
	mutate(cfg)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
