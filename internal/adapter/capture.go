package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JingxuanC/anchor/internal/core"
)

type CaptureResult struct {
	SchemaVersion         int    `json:"schema_version"`
	Adapter               string `json:"adapter"`
	Event                 string `json:"event"`
	SessionRef            string `json:"session_ref,omitempty"`
	CwdRef                string `json:"cwd_ref,omitempty"`
	Tool                  string `json:"tool,omitempty"`
	Timestamp             string `json:"timestamp,omitempty"`
	ContentPolicy         string `json:"content_policy"`
	NativeHookVerified    bool   `json:"native_hook_verified"`
	DoesNotRestoreOrBlock bool   `json:"does_not_restore_or_block"`
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

// Capture normalizes only allowlisted metadata. Prompts, tool inputs,
// transcripts, and arbitrary fields are never copied. Recognizable secrets
// anywhere in the input are rejected before normalization.
func Capture(adapterID string, input []byte) (CaptureResult, error) {
	a, err := Get(adapterID)
	if err != nil {
		return CaptureResult{}, err
	}
	if err := core.ScanSecrets(input); err != nil {
		return CaptureResult{}, fmt.Errorf("capture rejected: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return CaptureResult{}, fmt.Errorf("capture event must be a JSON object: %w", err)
	}
	event := strings.TrimSpace(stringField(m, "hook_event_name", "event"))
	if event == "" {
		return CaptureResult{}, fmt.Errorf("capture event has no hook_event_name/event")
	}
	tool := strings.TrimSpace(stringField(m, "tool_name", "tool"))
	if tool != "" {
		if err := core.ScanSecrets([]byte(tool)); err != nil {
			return CaptureResult{}, fmt.Errorf("capture metadata rejected: %w", err)
		}
	}
	return CaptureResult{
		SchemaVersion: 1, Adapter: a.ID, Event: event,
		SessionRef: hashRef(stringField(m, "session_id")),
		CwdRef:     hashRef(stringField(m, "cwd")),
		Tool:       tool, Timestamp: stringField(m, "timestamp"),
		ContentPolicy:         "metadata_only: prompt/tool_input/transcript omitted",
		NativeHookVerified:    false,
		DoesNotRestoreOrBlock: true,
	}, nil
}
