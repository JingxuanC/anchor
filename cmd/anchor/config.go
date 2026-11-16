package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/adapter"
)

// Configuration management (CFG-001..008): one source of truth in
// <state>/config/config.json (overridable per user scope), rendered into
// per-agent managed blocks with plan/diff, drift detection, snapshots/rollback
// and secret *references* only.

const (
	configSchemaVersion = 1
	managedBegin        = "<!-- anchor:begin (managed by `anchor config`; edits inside this block are overwritten) -->"
	managedEnd          = "<!-- anchor:end -->"
)

// anchorConfig is the single source of truth for rendered agent configuration.
type anchorConfig struct {
	SchemaVersion int               `json:"schema_version"`
	Agents        []string          `json:"agents"` // enabled adapters
	Rules         anchorRules       `json:"rules"`
	MCP           anchorMCP         `json:"mcp"`
	Hooks         anchorHooks       `json:"hooks"`
	SecretRefs    map[string]string `json:"secret_refs,omitempty"`
	UpdatedAt     string            `json:"updated_at,omitempty"`
}

// anchorHooks controls native Claude Code hook installation (config-owned).
type anchorHooks struct {
	ClaudeCode   *bool `json:"claude_code,omitempty"`
	SessionStart *bool `json:"session_start,omitempty"`
}

type anchorRules struct {
	Start      string `json:"start,omitempty"`
	Completion string `json:"completion,omitempty"`
	Branch     string `json:"branch,omitempty"`
}

type anchorMCP struct {
	Server string `json:"server,omitempty"` // default: anchor
}

type renderState struct {
	SchemaVersion int        `json:"schema_version"`
	AppliedAt     string     `json:"applied_at"`
	Files         []rendered `json:"files"`
}
type rendered struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // rules|mcp|skill|snippet
	SHA256 string `json:"sha256"`
}

type configSnapshot struct {
	SchemaVersion int          `json:"schema_version"`
	CreatedAt     string       `json:"created_at"`
	Reason        string       `json:"reason"`
	Files         []rendered   `json:"files"`
	State         *renderState `json:"state,omitempty"`
}

type configOp struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Action string `json:"action"` // create|update|unchanged|delete|render-snippet
	Detail string `json:"detail,omitempty"`
}

func cmdConfig(args []string) {
	if len(args) < 1 {
		configUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "init":
		configInit(args[1:])
	case "show":
		configShow(args[1:])
	case "plan":
		configPlan(args[1:], false)
	case "apply":
		configPlan(args[1:], true)
	case "status":
		configStatus(args[1:])
	case "rollback":
		configRollback(args[1:])
	default:
		configUsage()
		os.Exit(2)
	}
}

func configUsage() {
	fmt.Fprintln(os.Stderr, `  anchor config init --dir DIR --repo PROJECT [--scope project|user] [--agents list] [--apply]
  anchor config show --dir DIR [--json]
  anchor config plan --dir DIR --repo PROJECT [--json] [--diff]
  anchor config apply --dir DIR --repo PROJECT [--apply] [--json]
  anchor config status --dir DIR --repo PROJECT [--json]
  anchor config rollback --dir DIR --repo PROJECT [--to TIMESTAMP] [--apply] [--json]`)
}

func configPathFor(stateDir, scope string) (string, error) {
	if scope == "user" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "anchor", "config.json"), nil
	}
	return filepath.Join(stateDir, "config", "config.json"), nil
}

// loadAnchorConfig merges the user-scope config with the project-scope config;
// project values override user values (REQ-CFG-005).
func loadAnchorConfig(stateDir string) (anchorConfig, string, error) {
	cfg := defaultAnchorConfig()
	projectPath := filepath.Join(stateDir, "config", "config.json")
	userPath := ""
	if base, err := os.UserConfigDir(); err == nil {
		userPath = filepath.Join(base, "anchor", "config.json")
	}
	for _, p := range []string{userPath, projectPath} {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return cfg, projectPath, err
		}
		var overlay anchorConfig
		if err := json.Unmarshal(b, &overlay); err != nil {
			return cfg, projectPath, fmt.Errorf("invalid config %s: %w", p, err)
		}
		applyOverlay(&cfg, overlay)
	}
	if err := validateSecretRefs(cfg.SecretRefs); err != nil {
		return cfg, projectPath, err
	}
	return cfg, projectPath, nil
}

// validateSecretRefs enforces REQ-CFG-004: configuration may only reference
// secrets, never contain their values.
func validateSecretRefs(refs map[string]string) error {
	pattern := regexp.MustCompile(`^(env|keychain|op|file):[A-Za-z0-9_./:@-]+$`)
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := refs[name]
		if !pattern.MatchString(ref) {
			return fmt.Errorf("secret_refs[%q] must be a reference like env:NAME, keychain:NAME, op://... or file:PATH (literal secret values are rejected)", name)
		}
	}
	return nil
}

func defaultAnchorConfig() anchorConfig {
	yes := true
	return anchorConfig{SchemaVersion: configSchemaVersion, Agents: []string{"claude-code"},
		MCP: anchorMCP{Server: "anchor"}, Hooks: anchorHooks{ClaudeCode: &yes, SessionStart: &yes}}
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func applyOverlay(cfg *anchorConfig, o anchorConfig) {
	if o.SchemaVersion != 0 {
		cfg.SchemaVersion = o.SchemaVersion
	}
	if len(o.Agents) > 0 {
		cfg.Agents = append([]string{}, o.Agents...)
	}
	if o.Rules.Start != "" {
		cfg.Rules.Start = o.Rules.Start
	}
	if o.Rules.Completion != "" {
		cfg.Rules.Completion = o.Rules.Completion
	}
	if o.Rules.Branch != "" {
		cfg.Rules.Branch = o.Rules.Branch
	}
	if o.MCP.Server != "" {
		cfg.MCP.Server = o.MCP.Server
	}
	if o.SecretRefs != nil {
		cfg.SecretRefs = o.SecretRefs
	}
	if o.Hooks.ClaudeCode != nil {
		cfg.Hooks.ClaudeCode = o.Hooks.ClaudeCode
	}
	if o.Hooks.SessionStart != nil {
		cfg.Hooks.SessionStart = o.Hooks.SessionStart
	}
}

func configInit(args []string) {
	fs := flag.NewFlagSet("config init", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", "", "project directory (required)")
	scope := fs.String("scope", "project", "project|user")
	agents := fs.String("agents", "claude-code", "comma-separated adapters (claude-code,codex,gemini-cli)")
	apply := fs.Bool("apply", false, "write the config file (default: plan)")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	store := openStore(*dir)
	if *scope != "project" && *scope != "user" {
		die(fmt.Errorf("invalid --scope %q (project|user)", *scope))
	}
	path, err := configPathFor(store.Dir, *scope)
	if err != nil {
		die(err)
	}
	cfg := defaultAnchorConfig()
	cfg.Agents = splitList(*agents)
	cfg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if *scope == "project" {
		if *repo == "" {
			die(fmt.Errorf("--repo is required for project scope"))
		}
		repoDir, err := filepath.EvalSymlinks(*repo)
		if err != nil {
			die(fmt.Errorf("repo: %w", err))
		}
		cfg.Agents = ensureAgents(cfg.Agents)
		_ = repoDir
	}
	out := map[string]any{"path": path, "scope": *scope, "config": cfg, "applied": *apply}
	if !*apply {
		if *asJSON {
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Printf("config init plan: write %s (agents: %s)\n  (plan only; re-run with --apply to write)\n", path, strings.Join(cfg.Agents, ","))
		return
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		die(err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("config written to %s (agents: %s)\n", path, strings.Join(cfg.Agents, ","))
}

func configShow(args []string) {
	fs := flag.NewFlagSet("config show", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	store := openStore(*dir)
	cfg, path, err := loadAnchorConfig(store.Dir)
	if err != nil {
		die(err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"path": path, "config": cfg}, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("config: %s\nagents: %s\nmcp server: %s\nhooks: claude-code=%v session-start=%v\n",
		path, strings.Join(cfg.Agents, ","), cfg.MCP.Server,
		boolOr(cfg.Hooks.ClaudeCode, true), boolOr(cfg.Hooks.SessionStart, true))
	if len(cfg.SecretRefs) > 0 {
		names := make([]string, 0, len(cfg.SecretRefs))
		for k := range cfg.SecretRefs {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Printf("secret refs (reference only, never rendered): %s\n", strings.Join(names, ","))
	}
}

func configPlan(args []string, applyMode bool) {
	fs := flag.NewFlagSet("config apply", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", "", "project directory (required)")
	apply := fs.Bool("apply", false, "write changes (plan by default)")
	asJSON := fs.Bool("json", false, "JSON output")
	showDiff := fs.Bool("diff", false, "show a diff of the managed block")
	_ = fs.Parse(args)
	if applyMode {
		*apply = true
	}
	store := openStore(*dir)
	if *repo == "" {
		die(fmt.Errorf("--repo is required"))
	}
	repoDir, err := filepath.EvalSymlinks(*repo)
	if err != nil {
		die(fmt.Errorf("repo: %w", err))
	}
	if info, err := os.Stat(repoDir); err != nil || !info.IsDir() {
		die(fmt.Errorf("repo %s is not a directory", *repo))
	}
	cfg, cfgPath, err := loadAnchorConfig(store.Dir)
	if err != nil {
		die(err)
	}
	if info, err := os.Lstat(cfgPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		die(fmt.Errorf("refusing symlinked config file %s", cfgPath))
	}
	ops, targets, err := buildConfigRender(store.Dir, repoDir, cfg)
	if err != nil {
		die(err)
	}
	if *showDiff {
		for _, t := range targets {
			if t.Kind != "rules" {
				continue
			}
			if diff := managedBlockDiff(t.Path, t.Block); diff != "" {
				fmt.Printf("--- %s\n%s\n", t.Path, diff)
			}
		}
	}
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"config": cfg, "repo": repoDir, "operations": ops, "applied": *apply}, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("config render plan (repo %s)\n", repoDir)
		for _, op := range ops {
			fmt.Printf("  %-8s %-10s %s%s\n", op.Action, op.Kind, op.Path, op.Detail)
		}
	}
	if !*apply {
		if !*asJSON {
			fmt.Println("  (plan only; re-run with --apply to write)")
		}
		return
	}
	if err := applyConfigRender(store.Dir, repoDir, cfg, targets); err != nil {
		die(err)
	}
	if !*asJSON {
		fmt.Println("config applied")
	}
}

// configTarget is one rendered file.
type configTarget struct {
	Kind  string
	Path  string
	Block string // managed rules block (rules files)
	Full  string // full file content (mcp/skill/snippet)
}

func buildConfigRender(stateDir, repoDir string, cfg anchorConfig) ([]configOp, []configTarget, error) {
	anchorPath, err := adapter.ResolveAnchorPath()
	if err != nil {
		return nil, nil, err
	}
	agents := ensureAgents(cfg.Agents)
	var ops []configOp
	var targets []configTarget
	rulesByAgent := map[string]string{
		"claude-code": "CLAUDE.md",
		"codex":       "AGENTS.md",
		"gemini-cli":  "GEMINI.md",
	}
	for _, agent := range agents {
		file, ok := rulesByAgent[agent]
		if !ok {
			return nil, nil, fmt.Errorf("unknown agent %q in config", agent)
		}
		block := renderRulesBlock(cfg, agent, stateDir, anchorPath)
		path := filepath.Join(repoDir, file)
		action, detail := planManagedBlock(path, block)
		ops = append(ops, configOp{Kind: "rules", Path: path, Action: action, Detail: detail})
		targets = append(targets, configTarget{Kind: "rules", Path: path, Block: block})
	}
	mcpPath := filepath.Join(repoDir, ".mcp.json")
	cfgJSON, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		cfg.MCP.Server: map[string]any{"command": anchorPath, "args": []any{"mcp", "serve", "--dir", stateDir}},
	}}, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	mcpContent := string(cfgJSON) + "\n"
	action, detail := planManagedMCP(mcpPath, cfg.MCP.Server, mcpContent)
	ops = append(ops, configOp{Kind: "mcp", Path: mcpPath, Action: action, Detail: detail})
	targets = append(targets, configTarget{Kind: "mcp", Path: mcpPath, Full: mcpContent})

	for _, agent := range agents {
		if agent != "claude-code" {
			// Codex/Gemini MCP snippets are rendered artifacts, not auto-merged.
			dir := filepath.Join(stateDir, "config", "rendered")
			var content string
			switch agent {
			case "codex":
				content = fmt.Sprintf("[mcp_servers.%s]\ncommand = %q\nargs = [\"mcp\", \"serve\", \"--dir\", %q]\n", cfg.MCP.Server, anchorPath, stateDir)
			case "gemini-cli":
				content = fmt.Sprintf("{\n  \"mcpServers\": {\n    \"%s\": {\"command\": %q, \"args\": [\"mcp\", \"serve\", \"--dir\", %q]}\n  }\n}\n", cfg.MCP.Server, anchorPath, stateDir)
			}
			name := "codex-config.toml"
			if agent == "gemini-cli" {
				name = "gemini-settings.json"
			}
			path := filepath.Join(dir, name)
			action := "create"
			if b, err := os.ReadFile(path); err == nil && string(b) == content {
				action = "unchanged"
			}
			ops = append(ops, configOp{Kind: "snippet", Path: path, Action: action, Detail: " (merge into the agent's MCP config)"})
			targets = append(targets, configTarget{Kind: "snippet", Path: path, Full: content})
		}
	}
	skillPath := filepath.Join(repoDir, ".claude", "skills", "anchor", "SKILL.md")
	action = "create"
	if b, err := os.ReadFile(skillPath); err == nil && string(b) == adapter.SkillMarkdown {
		action = "unchanged"
	}
	ops = append(ops, configOp{Kind: "skill", Path: skillPath, Action: action})
	targets = append(targets, configTarget{Kind: "skill", Path: skillPath, Full: adapter.SkillMarkdown})

	// Native hooks are config-owned (CFG-001): config apply installs/removes the
	// managed Claude Code hook entries; adapter install remains a standalone
	// shortcut for hook-only setups.
	if len(agents) > 0 && agents[0] == "claude-code" || containsAgent(agents, "claude-code") {
		hooksWanted := boolOr(cfg.Hooks.ClaudeCode, true)
		includeSession := boolOr(cfg.Hooks.SessionStart, true)
		plan, err := adapter.Install(adapter.InstallOptions{AdapterID: "claude-code", StateDir: stateDir,
			RepoDir: repoDir, AnchorPath: anchorPath, NoSessionContext: !includeSession})
		if err != nil {
			return nil, nil, err
		}
		settingsPath := filepath.Join(repoDir, ".claude", "settings.json")
		switch {
		case !hooksWanted:
			if st, _ := adapter.LoadInstallState(stateDir, "claude-code"); st != nil {
				ops = append(ops, configOp{Kind: "hooks", Path: settingsPath, Action: "uninstall", Detail: " (hooks disabled in config)"})
				targets = append(targets, configTarget{Kind: "hooks", Path: settingsPath})
			} else {
				ops = append(ops, configOp{Kind: "hooks", Path: settingsPath, Action: "unchanged", Detail: " (disabled)"})
			}
		case plan.AlreadyInstalled:
			ops = append(ops, configOp{Kind: "hooks", Path: settingsPath, Action: "unchanged", Detail: " (managed hooks installed)"})
			targets = append(targets, configTarget{Kind: "hooks", Path: settingsPath})
		default:
			ops = append(ops, configOp{Kind: "hooks", Path: settingsPath, Action: "update", Detail: " (managed hooks: SessionStart/PostToolUse/SessionEnd)"})
			targets = append(targets, configTarget{Kind: "hooks", Path: settingsPath})
		}
	}
	return ops, targets, nil
}

func containsAgent(agents []string, want string) bool {
	for _, a := range agents {
		if a == want {
			return true
		}
	}
	return false
}

func planManagedBlock(path, block string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "create", ""
	}
	old, ok := extractManagedBlock(string(b))
	if !ok {
		return "update", " (insert managed block, preserving user content)"
	}
	if old == block {
		return "unchanged", ""
	}
	return "update", " (managed block changed)"
}

func extractManagedBlock(content string) (string, bool) {
	start := strings.Index(content, managedBegin)
	if start < 0 {
		return "", false
	}
	end := strings.Index(content[start:], managedEnd)
	if end < 0 {
		return "", false
	}
	return content[start : start+end+len(managedEnd)], true
}

func mergeManagedBlock(content, block string) string {
	if old, ok := extractManagedBlock(content); ok {
		return strings.Replace(content, old, block, 1)
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + "\n" + block + "\n"
}

func planManagedMCP(path, server, desired string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "create", ""
	}
	var existing map[string]any
	if err := json.Unmarshal(b, &existing); err != nil {
		return "update", " (existing .mcp.json is not a JSON object; refusing to change it)"
	}
	servers, _ := existing["mcpServers"].(map[string]any)
	if servers != nil {
		if cur, ok := servers[server]; ok {
			curJSON, _ := json.Marshal(cur)
			var desiredObj map[string]any
			_ = json.Unmarshal([]byte(desired), &desiredObj)
			desiredServer, _ := json.Marshal(desiredObj["mcpServers"].(map[string]any)[server])
			if string(curJSON) == string(desiredServer) {
				return "unchanged", ""
			}
		}
	}
	return "update", " (merge managed server, preserving other servers)"
}

func applyConfigRender(stateDir, repoDir string, cfg anchorConfig, targets []configTarget) error {
	if err := configSnapshotFiles(stateDir, repoDir, targets, "pre-apply"); err != nil {
		return err
	}
	state := renderState{SchemaVersion: configSchemaVersion, AppliedAt: time.Now().UTC().Format(time.RFC3339)}
	for _, t := range targets {
		if t.Kind == "hooks" {
			// Native hooks are installed/removed through adapter.Install below; the
			// target exists so snapshots capture .claude/settings.json.
			continue
		}
		var content string
		switch t.Kind {
		case "rules":
			existing, err := os.ReadFile(t.Path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			content = mergeManagedBlock(string(existing), t.Block)
		default:
			content = t.Full
		}
		if t.Kind == "rules" || t.Kind == "mcp" {
			if err := backupBeforeWrite(t.Path); err != nil {
				return err
			}
		}
		if err := secureWriteFile(t.Path, content); err != nil {
			return err
		}
		state.Files = append(state.Files, rendered{Path: t.Path, Kind: t.Kind, SHA256: hashString(content)})
	}
	anchorPath, err := adapter.ResolveAnchorPath()
	if err != nil {
		return err
	}
	if containsAgent(ensureAgents(cfg.Agents), "claude-code") {
		if !boolOr(cfg.Hooks.ClaudeCode, true) {
			if st, _ := adapter.LoadInstallState(stateDir, "claude-code"); st != nil {
				if _, err := adapter.Install(adapter.InstallOptions{AdapterID: "claude-code", StateDir: stateDir,
					RepoDir: repoDir, AnchorPath: anchorPath, Uninstall: true, Apply: true}); err != nil {
					return fmt.Errorf("cannot remove managed hooks: %w", err)
				}
			}
		} else {
			if _, err := adapter.Install(adapter.InstallOptions{AdapterID: "claude-code", StateDir: stateDir,
				RepoDir: repoDir, AnchorPath: anchorPath, Apply: true,
				NoSessionContext: !boolOr(cfg.Hooks.SessionStart, true)}); err != nil {
				return fmt.Errorf("cannot install managed hooks: %w", err)
			}
		}
	}
	return writeJSONAtomic(filepath.Join(stateDir, "config", "state.json"), state)
}

func configSnapshotFiles(stateDir, repoDir string, targets []configTarget, reason string) error {
	ts := time.Now().UTC().Format("20060102T150405.000000000")
	dir := filepath.Join(stateDir, "config", "snapshots", ts)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	snap := configSnapshot{SchemaVersion: configSchemaVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339), Reason: reason}
	for _, t := range targets {
		b, err := os.ReadFile(t.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		name := strings.ReplaceAll(filepath.ToSlash(strings.TrimPrefix(t.Path, repoDir+string(filepath.Separator))), "/", "__")
		if name == "" || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			name = fmt.Sprintf("%s-%d", t.Kind, len(snap.Files))
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), b, 0o600); err != nil {
			return err
		}
		snap.Files = append(snap.Files, rendered{Path: t.Path, Kind: t.Kind, SHA256: hashString(string(b))})
	}
	return writeJSONAtomic(filepath.Join(dir, "snapshot.json"), snap)
}

func configStatus(args []string) {
	fs := flag.NewFlagSet("config status", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", "", "project directory (required)")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	store := openStore(*dir)
	if *repo == "" {
		die(fmt.Errorf("--repo is required"))
	}
	repoDir, err := filepath.EvalSymlinks(*repo)
	if err != nil {
		die(fmt.Errorf("repo: %w", err))
	}
	cfg, _, err := loadAnchorConfig(store.Dir)
	if err != nil {
		die(err)
	}
	_, targets, err := buildConfigRender(store.Dir, repoDir, cfg)
	if err != nil {
		die(err)
	}
	var state renderState
	if b, err := os.ReadFile(filepath.Join(store.Dir, "config", "state.json")); err == nil {
		if err := json.Unmarshal(b, &state); err != nil {
			die(err)
		}
	}
	recorded := map[string]string{}
	for _, f := range state.Files {
		recorded[f.Path] = f.SHA256
	}
	type fileStatus struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Status string `json:"status"` // ok|drift|missing|never-applied
	}
	var statuses []fileStatus
	for _, t := range targets {
		if t.Kind == "hooks" {
			// Derived from the adapter install state below, not from a file hash.
			continue
		}
		st := fileStatus{Path: t.Path, Kind: t.Kind}
		b, err := os.ReadFile(t.Path)
		if err != nil {
			if os.IsNotExist(err) {
				st.Status = "missing"
			} else {
				die(err)
			}
		} else {
			switch t.Kind {
			case "rules":
				// Drift is about the managed block only: user content outside it is
				// never considered drift.
				block, ok := extractManagedBlock(string(b))
				if !ok || block != t.Block {
					st.Status = "drift"
				} else {
					st.Status = "ok"
				}
			case "mcp", "skill", "snippet":
				if string(b) != t.Full {
					st.Status = "drift"
				} else {
					st.Status = "ok"
				}
			default:
				st.Status = "drift"
			}
			if _, ok := recorded[t.Path]; !ok && st.Status == "drift" {
				st.Status = "never-applied"
			}
		}
		statuses = append(statuses, st)
	}
	// Hooks status is derived from the adapter install state (config-owned).
	if containsAgent(ensureAgents(cfg.Agents), "claude-code") {
		st := fileStatus{Path: filepath.Join(repoDir, ".claude", "settings.json"), Kind: "hooks"}
		installed, _ := adapter.LoadInstallState(store.Dir, "claude-code")
		wantHooks := boolOr(cfg.Hooks.ClaudeCode, true)
		switch {
		case !wantHooks && installed == nil:
			st.Status = "ok"
		case !wantHooks && installed != nil:
			st.Status = "drift"
		case installed == nil:
			st.Status = "missing"
		default:
			hasSession := false
			for _, e := range installed.Entries {
				if e.Event == "SessionStart" {
					hasSession = true
				}
			}
			if boolOr(cfg.Hooks.SessionStart, true) != hasSession {
				st.Status = "drift"
			} else {
				st.Status = "ok"
			}
		}
		statuses = append(statuses, st)
	}
	drift := false
	for _, s := range statuses {
		if s.Status != "ok" {
			drift = true
		}
	}
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"clean": !drift, "files": statuses, "config_agents": cfg.Agents}, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("config status (repo %s)\n", repoDir)
	for _, s := range statuses {
		fmt.Printf("  %-12s %-8s %s\n", s.Status, s.Kind, s.Path)
	}
	if drift {
		fmt.Println("drift detected: run `anchor config plan --diff` then `anchor config apply --apply`")
	}
}

func configRollback(args []string) {
	fs := flag.NewFlagSet("config rollback", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", "", "project directory (required)")
	to := fs.String("to", "", "snapshot timestamp (default: latest)")
	apply := fs.Bool("apply", false, "restore the snapshot (default: plan)")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	store := openStore(*dir)
	if *repo == "" {
		die(fmt.Errorf("--repo is required"))
	}
	repoDir, err := filepath.EvalSymlinks(*repo)
	if err != nil {
		die(fmt.Errorf("repo: %w", err))
	}
	dirs, err := filepath.Glob(filepath.Join(store.Dir, "config", "snapshots", "*"))
	if err != nil || len(dirs) == 0 {
		die(fmt.Errorf("no config snapshots to roll back to"))
	}
	sort.Strings(dirs)
	chosen := dirs[len(dirs)-1]
	if *to != "" {
		chosen = filepath.Join(store.Dir, "config", "snapshots", *to)
		if _, err := os.Stat(chosen); err != nil {
			die(fmt.Errorf("snapshot %s not found", *to))
		}
	}
	var snap configSnapshot
	b, err := os.ReadFile(filepath.Join(chosen, "snapshot.json"))
	if err != nil {
		die(err)
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		die(err)
	}
	type action struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	}
	var actions []action
	for _, f := range snap.Files {
		name := strings.ReplaceAll(filepath.ToSlash(strings.TrimPrefix(f.Path, repoDir+string(filepath.Separator))), "/", "__")
		src := filepath.Join(chosen, filepath.Base(name))
		if _, err := os.Stat(src); err != nil {
			continue
		}
		actions = append(actions, action{Path: f.Path, Kind: f.Kind, Status: "restore"})
	}
	if *asJSON {
		out := map[string]any{"snapshot": filepath.Base(chosen), "actions": actions, "applied": *apply}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("config rollback plan: snapshot %s\n", filepath.Base(chosen))
		for _, a := range actions {
			fmt.Printf("  restore %-8s %s\n", a.Kind, a.Path)
		}
		if !*apply {
			fmt.Println("  (plan only; re-run with --apply to restore)")
		}
	}
	if !*apply {
		return
	}
	for _, f := range snap.Files {
		name := strings.ReplaceAll(filepath.ToSlash(strings.TrimPrefix(f.Path, repoDir+string(filepath.Separator))), "/", "__")
		src := filepath.Join(chosen, filepath.Base(name))
		if _, err := os.Stat(src); err != nil {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			die(err)
		}
		if err := secureWriteFile(f.Path, string(data)); err != nil {
			die(err)
		}
	}
	if snap.State != nil {
		if err := writeJSONAtomic(filepath.Join(store.Dir, "config", "state.json"), *snap.State); err != nil {
			die(err)
		}
	}
	if !*asJSON {
		fmt.Println("config rolled back")
	}
}

// ensureAnchorConfig writes a default config file if none exists yet, enabling
// the given agents. It returns the project-scope config path.
func ensureAnchorConfig(stateDir string, agents []string) (string, error) {
	path := filepath.Join(stateDir, "config", "config.json")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	cfg := defaultAnchorConfig()
	cfg.Agents = ensureAgents(agents)
	cfg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeJSONAtomic(path, cfg); err != nil {
		return "", err
	}
	return path, nil
}

// applyAnchorConfig renders and applies the current config for a repository.
func applyAnchorConfig(stateDir, repoDir string) ([]configOp, error) {
	cfg, _, err := loadAnchorConfig(stateDir)
	if err != nil {
		return nil, err
	}
	ops, targets, err := buildConfigRender(stateDir, repoDir, cfg)
	if err != nil {
		return nil, err
	}
	if err := applyConfigRender(stateDir, repoDir, cfg, targets); err != nil {
		return nil, err
	}
	return ops, nil
}

// renderRulesBlock builds the managed rules block rendered into each agent's
// rules file. It instructs the agent to use Anchor at start, completion and
// branch points (REQ-CFG-007) and lists the MCP tools.
func renderRulesBlock(cfg anchorConfig, agent, stateDir, anchorPath string) string {
	start := cfg.Rules.Start
	if start == "" {
		start = "At session start read `ANCHOR_CONTEXT_FILE` (when wrapped) and run `anchor status --json` and `anchor tree --dir \"$ANCHOR_WORKSPACE\"`. If a handoff is pending, read the plan with `anchor handoff restore` before touching files."
	}
	completion := cfg.Rules.Completion
	if completion == "" {
		completion = "At session end record unresolved work as task/question nodes, run `anchor handoff prepare --dir \"$ANCHOR_WORKSPACE\" --repo .` and report the packet path. When resuming, acknowledge and verify the packet before editing."
	}
	branch := cfg.Rules.Branch
	if branch == "" {
		branch = "When you change direction or branch, record a decision node with rationale and consequences instead of relying on chat history."
	}
	var b strings.Builder
	b.WriteString(managedBegin + "\n")
	fmt.Fprintf(&b, "## Anchor workspace rules (agent: %s)\n\n", agent)
	b.WriteString("Anchor is the durable workspace: the Session Tree stores goals, constraints, decisions, tasks and evidence across sessions/agents.\n\n")
	b.WriteString("- workspace: `" + stateDir + "`\n")
	b.WriteString("- binary: `" + anchorPath + "`\n")
	fmt.Fprintf(&b, "\n**Start.** %s\n\n", start)
	fmt.Fprintf(&b, "**During work.** Record decisions (`anchor node create --type decision … --rationale …`), task transitions and verifiable evidence (`anchor evidence add`). Never record secrets, tokens, prompts or transcripts. %s\n\n", branch)
	fmt.Fprintf(&b, "**Completion.** %s\n\n", completion)
	b.WriteString("**MCP tools (read-only):** `anchor_status`, `anchor_tree`, `anchor_evidence_verify`, `anchor_restore_plan`. Mutations go through the CLI so the successor gate, ownership fencing and event validation apply.\n\n")
	if agent == "claude-code" {
		b.WriteString("**Agent skill.** `.claude/skills/anchor/SKILL.md` is installed by `anchor config apply`; follow it.\n")
	} else {
		// Activation semantics are adapter-specific: Codex and Gemini read their
		// rules file, so the skill playbook is rendered here instead of assuming a
		// skill directory exists (REQ-CFG-008).
		b.WriteString("**Agent skill (activated through this rules file).**\n")
		b.WriteString("- Start: read `ANCHOR_CONTEXT_FILE` when present, then `anchor status --json` and `anchor tree`; if a handoff is pending, read `anchor handoff restore` before editing.\n")
		b.WriteString("- Work: record decisions (`--type decision --rationale …`), task transitions and verifiable evidence with the CLI; never record secrets, tokens or transcripts.\n")
		b.WriteString("- Finish: `anchor handoff prepare --dir \"$ANCHOR_WORKSPACE\" --repo .` and record unresolved work as task/question nodes.\n")
		b.WriteString("- Resume: acknowledge and verify the handoff packet before editing; apply packaged code with `anchor handoff apply-code --apply`.\n")
	}
	b.WriteString(managedEnd)
	return b.String()
}

// managedBlockDiff returns a simple line diff between the current managed block
// (if any) and the desired block.
func managedBlockDiff(path, block string) string {
	existing, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	old, ok := extractManagedBlock(string(existing))
	if !ok {
		return "(no managed block; apply inserts it)"
	}
	if old == block {
		return ""
	}
	var b strings.Builder
	b.WriteString("  current managed block:\n")
	for _, line := range limitLines(strings.Split(old, "\n"), 40) {
		b.WriteString("  - " + line + "\n")
	}
	b.WriteString("  desired managed block:\n")
	for _, line := range limitLines(strings.Split(block, "\n"), 40) {
		b.WriteString("  + " + line + "\n")
	}
	return b.String()
}

func limitLines(lines []string, max int) []string {
	if len(lines) <= max {
		return lines
	}
	return append(lines[:max], "…")
}

func ensureAgents(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func backupBeforeWrite(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backup := path + ".anchor-backup-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err := os.WriteFile(backup, b, 0o600); err != nil {
		return err
	}
	return nil
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return secureWriteFile(path, string(b)+"\n")
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
