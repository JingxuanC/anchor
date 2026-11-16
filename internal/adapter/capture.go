package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
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
	SourceEventRef        string `json:"source_event_ref,omitempty"`
	SourceSeq             uint64 `json:"source_seq,omitempty"`
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

func uintField(m map[string]any, keys ...string) (uint64, error) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case float64:
			if x < 0 || x != math.Trunc(x) || x > math.MaxUint64 {
				return 0, fmt.Errorf("source_seq must be a non-negative integer")
			}
			return uint64(x), nil
		case string:
			n, err := strconv.ParseUint(x, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("source_seq must be a non-negative integer")
			}
			return n, nil
		default:
			return 0, fmt.Errorf("source_seq must be a non-negative integer")
		}
	}
	return 0, nil
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
	sessionRef := hashRef(stringField(m, "session_id"))
	sourceID := stringField(m, "source_event_id", "hook_event_id", "event_id")
	sourceSeq, err := uintField(m, "source_seq")
	if err != nil {
		return CaptureResult{}, err
	}
	sourceRef := hashRef(sourceID)
	if sourceRef == "" && sourceSeq > 0 {
		sourceRef = hashRef(fmt.Sprintf("source-seq|%s|%s|%d", a.ID, sessionRef, sourceSeq))
	}
	return CaptureResult{
		SchemaVersion: 1, Adapter: a.ID, Event: event,
		SessionRef: sessionRef,
		CwdRef:     hashRef(stringField(m, "cwd")),
		Tool:       tool, Timestamp: stringField(m, "timestamp"),
		SourceEventRef: sourceRef, SourceSeq: sourceSeq,
		ContentPolicy:         "metadata_only: prompt/tool_input/transcript omitted",
		NativeHookVerified:    false,
		DoesNotRestoreOrBlock: true,
	}, nil
}

func SessionID(adapterID, sessionRef string) string {
	return core.AdapterSessionID(adapterID, sessionRef)
}

func IsSessionEnd(event string) bool {
	n := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(event)))
	return n == "sessionend"
}

// IdempotencyKey binds the normalized capture, explicit node mapping, and
// source. The key is a hash, so it does not store raw session ids or content.
func IdempotencyKey(adapterID, nodeID, source string, result CaptureResult) (string, error) {
	// SourceEventRef is the stable delivery identity. Do not include mutable
	// metadata or source_seq in the key: the same source event delivered twice
	// with a changed sequence must still deduplicate.
	payload := struct {
		Adapter        string `json:"adapter"`
		NodeID         string `json:"node_id"`
		Source         string `json:"source"`
		SessionRef     string `json:"session_ref"`
		SourceEventRef string `json:"source_event_ref"`
	}{adapterID, nodeID, source, result.SessionRef, result.SourceEventRef}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "adapter_capture:" + hex.EncodeToString(sum[:]), nil
}
