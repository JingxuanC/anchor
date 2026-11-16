package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func mcpConfigFixture(t *testing.T) (root, proj string) {
	return treeFixture(t)
}

func TestCLIMcpConfigSnippets(t *testing.T) {
	root, proj := mcpConfigFixture(t)
	out, stderr, code := runCLI(t, root, "mcp", "config", "--adapter", "claude-code", "--dir", proj, "--json")
	if code != 0 {
		t.Fatalf("claude config: %s%s", out, stderr)
	}
	var cfg struct {
		AdapterID string   `json:"adapter_id"`
		Format    string   `json:"format"`
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		Snippet   string   `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AdapterID != "claude-code" || cfg.Format != "json" || !filepath.IsAbs(cfg.Command) {
		t.Fatalf("claude config wrong: %s", out)
	}
	if len(cfg.Args) != 4 || cfg.Args[0] != "mcp" || cfg.Args[1] != "serve" || cfg.Args[2] != "--dir" {
		t.Fatalf("args wrong: %v", cfg.Args)
	}
	stateDir, _ := filepath.EvalSymlinks(filepath.Join(proj, ".anchor"))
	if cfg.Args[3] != stateDir {
		t.Fatalf("state dir = %q, want %q", cfg.Args[3], stateDir)
	}
	var snippet map[string]any
	if err := json.Unmarshal([]byte(cfg.Snippet), &snippet); err != nil {
		t.Fatalf("claude snippet is not JSON: %v", err)
	}
	servers := snippet["mcpServers"].(map[string]any)
	if _, ok := servers["anchor"]; !ok {
		t.Fatalf("snippet missing anchor server: %s", cfg.Snippet)
	}

	codexOut, stderr, code := runCLI(t, root, "mcp", "config", "--adapter", "codex", "--dir", proj, "--json")
	if code != 0 {
		t.Fatalf("codex config: %s%s", codexOut, stderr)
	}
	var codexCfg struct {
		Format  string `json:"format"`
		Snippet string `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(codexOut), &codexCfg); err != nil {
		t.Fatal(err)
	}
	if codexCfg.Format != "toml" || !strings.Contains(codexCfg.Snippet, "[mcp_servers.anchor]") ||
		!strings.Contains(codexCfg.Snippet, `args = ["mcp", "serve", "--dir"`) {
		t.Fatalf("codex snippet wrong: %s", codexCfg.Snippet)
	}

	geminiOut, stderr, code := runCLI(t, root, "mcp", "config", "--adapter", "gemini-cli", "--dir", proj)
	if code != 0 {
		t.Fatalf("gemini config: %s%s", geminiOut, stderr)
	}
	if !strings.Contains(geminiOut, "mcpServers") || !strings.Contains(geminiOut, "anchor") {
		t.Fatalf("gemini snippet wrong: %s", geminiOut)
	}
}

func TestCLIMcpConfigRejectsUnknownAdapter(t *testing.T) {
	root, proj := treeFixture(t)
	if _, stderr, code := runCLI(t, root, "mcp", "config", "--adapter", "nope", "--dir", proj); code != 1 {
		t.Fatalf("unknown adapter accepted: %s", stderr)
	}
}
