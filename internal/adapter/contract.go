package adapter

import (
	"fmt"
	"sort"
)

type Feature struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Capability struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	EvidenceGrade    string    `json:"evidence_grade"`
	NativeHookStatus string    `json:"native_hook_status"`
	EndToEndVerified bool      `json:"end_to_end_verified"`
	ConfigTargets    []string  `json:"config_targets"`
	CaptureFields    []string  `json:"capture_fields"`
	Features         []Feature `json:"features"`
	NotClaimed       []string  `json:"not_claimed"`
}

// D = documentation reviewed; V = verified in this environment; S = supported
// end-to-end. No adapter in this build is V or S.
var registry = map[string]Capability{
	"claude-code": {
		ID: "claude-code", Name: "Claude Code",
		EvidenceGrade: "D", NativeHookStatus: "unverified", EndToEndVerified: false,
		ConfigTargets: []string{"settings.json"},
		CaptureFields: []string{"hook_event_name", "session_id", "cwd", "tool_name", "timestamp"},
		Features: []Feature{
			{Name: "hooks", Status: "documented_unverified"},
			{Name: "context_injection", Status: "documented_unverified"},
		},
		NotClaimed: []string{"automatic installation", "verified native hook", "restore or enforcement", "MCP proxy"},
	},
	"codex": {
		ID: "codex", Name: "Codex CLI",
		EvidenceGrade: "D", NativeHookStatus: "unverified", EndToEndVerified: false,
		ConfigTargets: []string{"config.toml"},
		CaptureFields: []string{"event", "session_id", "cwd", "tool_name", "timestamp"},
		Features: []Feature{
			{Name: "config_toml", Status: "documented_unverified"},
			{Name: "hook_lifecycle", Status: "documented_unverified"},
		},
		NotClaimed: []string{"automatic installation", "verified native hook", "restore or enforcement", "MCP proxy"},
	},
	"gemini-cli": {
		ID: "gemini-cli", Name: "Gemini CLI",
		EvidenceGrade: "D", NativeHookStatus: "unverified", EndToEndVerified: false,
		ConfigTargets: []string{"settings.json"},
		CaptureFields: []string{"hook_event_name", "session_id", "cwd", "tool_name", "timestamp"},
		Features: []Feature{
			{Name: "hooks", Status: "documented_unverified"},
			{Name: "settings_json", Status: "documented_unverified"},
		},
		NotClaimed: []string{"automatic installation", "verified native hook", "restore or enforcement", "MCP proxy"},
	},
}

func All() []Capability {
	out := make([]Capability, 0, len(registry))
	for _, a := range registry {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func Get(id string) (Capability, error) {
	a, ok := registry[id]
	if !ok {
		return Capability{}, fmt.Errorf("unknown adapter %q", id)
	}
	return a, nil
}
