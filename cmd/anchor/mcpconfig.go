package main

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/JingxuanC/anchor/internal/adapter"
)

// mcpConfigResult is the per-agent registration snippet printed by
// `anchor mcp config`.
type mcpConfigResult struct {
	AdapterID string   `json:"adapter_id"`
	Format    string   `json:"format"`
	Target    string   `json:"target"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Snippet   string   `json:"snippet"`
}

// cmdMcpConfig prints the MCP server registration for an agent CLI. It never
// writes shared agent config; `adapter install --with-mcp` owns the Claude Code
// project registration.
func cmdMcpConfig(args []string) {
	fs := flag.NewFlagSet("mcp config", flag.ExitOnError)
	adapterID := fs.String("adapter", "claude-code", "claude-code|codex|gemini-cli")
	dir := fs.String("dir", ".", "workspace directory")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	store := openStore(*dir)
	anchorPath, err := adapter.ResolveAnchorPath()
	if err != nil {
		die(err)
	}
	result := mcpConfigResult{AdapterID: *adapterID, Command: anchorPath,
		Args: []string{"mcp", "serve", "--dir", store.Dir}}

	switch *adapterID {
	case "claude-code":
		result.Format = "json"
		result.Target = "project .mcp.json (or `anchor adapter install --with-mcp` to merge it automatically)"
		payload := map[string]any{"mcpServers": map[string]any{
			"anchor": map[string]any{"command": anchorPath, "args": result.Args},
		}}
		b, _ := json.MarshalIndent(payload, "", "  ")
		result.Snippet = string(b) + "\n"
	case "codex":
		result.Format = "toml"
		result.Target = "~/.codex/config.toml (or a project scope config)"
		result.Snippet = fmt.Sprintf("[mcp_servers.anchor]\ncommand = %q\nargs = [%q, %q, %q, %q]\n",
			anchorPath, result.Args[0], result.Args[1], result.Args[2], result.Args[3])
	case "gemini-cli":
		result.Format = "json"
		result.Target = "~/.gemini/settings.json (mcpServers block)"
		payload := map[string]any{"mcpServers": map[string]any{
			"anchor": map[string]any{"command": anchorPath, "args": result.Args},
		}}
		b, _ := json.MarshalIndent(payload, "", "  ")
		result.Snippet = string(b) + "\n"
	default:
		die(fmt.Errorf("unknown --adapter %q (claude-code|codex|gemini-cli)", *adapterID))
	}

	if *asJSON {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("# adapter: %s (%s)\n# target: %s\n# workspace: %s\n\n%s",
		result.AdapterID, result.Format, result.Target, store.Dir, result.Snippet)
	if *adapterID == "claude-code" {
		fmt.Println("\n# automatic (project scope): anchor adapter install --adapter claude-code \\")
		fmt.Println("#   --dir <state> --repo <project> --with-mcp --apply")
		fmt.Println("# verify from the agent: it should see tools anchor_status / anchor_tree /")
		fmt.Println("#   anchor_evidence_verify / anchor_restore_plan")
	}
}
