package adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// shaRef independently computes the sha256 reference format the package uses.
func shaRef(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// sessionIDFor independently computes the Session Tree key format.
func sessionIDFor(adapterID, sessionRef string) string {
	sum := sha256.Sum256([]byte("anchor-session|" + adapterID + "|" + sessionRef))
	return "sess_" + hex.EncodeToString(sum[:8])
}

func mustCapture(t *testing.T, adapterID, raw string) CaptureResult {
	t.Helper()
	got, err := Capture(adapterID, []byte(raw))
	if err != nil {
		t.Fatalf("Capture(%q, %s): %v", adapterID, raw, err)
	}
	return got
}

func decodeJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, data)
	}
	return m
}

func jsonObject(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("JSON value is %T, want object", v)
	}
	return m
}

func jsonArray(t *testing.T, v any) []any {
	t.Helper()
	a, ok := v.([]any)
	if !ok {
		t.Fatalf("JSON value is %T, want array", v)
	}
	return a
}

func TestSessionID(t *testing.T) {
	ref := shaRef("native-session-1")
	adapters := []string{"claude-code", "codex", "gemini-cli"}

	for _, id := range adapters {
		t.Run(id, func(t *testing.T) {
			want := sessionIDFor(id, ref)
			got := SessionID(id, ref)
			if got != want {
				t.Fatalf("SessionID(%q, ref) = %q, want %q", id, got, want)
			}
			if again := SessionID(id, ref); again != got {
				t.Fatalf("SessionID is not deterministic: %q vs %q", got, again)
			}
			if !strings.HasPrefix(got, "sess_") {
				t.Fatalf("SessionID %q lacks sess_ prefix", got)
			}
			if len(got) != len("sess_")+16 {
				t.Fatalf("SessionID %q has unexpected length %d", got, len(got))
			}
		})
	}

	if SessionID("claude-code", ref) == SessionID("codex", ref) {
		t.Fatal("different adapters produced the same SessionID")
	}
	if SessionID("claude-code", ref) == SessionID("claude-code", shaRef("native-session-2")) {
		t.Fatal("different session refs produced the same SessionID")
	}
	if got, want := SessionID("codex", ""), sessionIDFor("codex", ""); got != want {
		t.Fatalf("SessionID with empty ref = %q, want %q", got, want)
	}
}

func TestIsSessionEnd(t *testing.T) {
	cases := []struct {
		event string
		want  bool
	}{
		{"SessionEnd", true},
		{"sessionend", true},
		{"session_end", true},
		{"session-end", true},
		{"SESSION-END", true},
		{"  SessionEnd  ", true},
		{"session__end", true},
		{"Session_End-", true},
		{"", false},
		{"   ", false},
		{"session", false},
		{"end", false},
		{"SessionStart", false},
		{"session_start", false},
		{"pre_compact", false},
		{"stop", false},
		{"post_tool_use", false},
		{"session end", false},
		{"endsession", false},
		{"sessionendmarker", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.event), func(t *testing.T) {
			if got := IsSessionEnd(tc.event); got != tc.want {
				t.Fatalf("IsSessionEnd(%q) = %v, want %v", tc.event, got, tc.want)
			}
		})
	}
}

func TestCaptureErrorPaths(t *testing.T) {
	cases := []struct {
		name       string
		adapterID  string
		input      string
		wantErr    string
		wantNoEcho []string
	}{
		{
			name:      "unknown adapter",
			adapterID: "not-an-adapter",
			input:     `{"hook_event_name":"PostToolUse"}`,
			wantErr:   `unknown adapter "not-an-adapter"`,
		},
		{
			name:      "malformed JSON",
			adapterID: "claude-code",
			input:     `{"hook_event_name":`,
			wantErr:   "must be a JSON object",
		},
		{
			name:      "JSON array",
			adapterID: "claude-code",
			input:     `[1,2,3]`,
			wantErr:   "must be a JSON object",
		},
		{
			name:      "JSON string",
			adapterID: "claude-code",
			input:     `"PostToolUse"`,
			wantErr:   "must be a JSON object",
		},
		{
			name:      "JSON number",
			adapterID: "claude-code",
			input:     `42`,
			wantErr:   "must be a JSON object",
		},
		{
			name:      "JSON null",
			adapterID: "claude-code",
			input:     `null`,
			wantErr:   "no hook_event_name/event",
		},
		{
			name:      "missing event",
			adapterID: "claude-code",
			input:     `{"session_id":"s"}`,
			wantErr:   "no hook_event_name/event",
		},
		{
			name:      "blank event",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"   "}`,
			wantErr:   "no hook_event_name/event",
		},
		{
			name:       "secret in prompt",
			adapterID:  "claude-code",
			input:      `{"hook_event_name":"PostToolUse","prompt":"-----BEGIN RSA PRIVATE KEY-----"}`,
			wantErr:    "capture rejected",
			wantNoEcho: []string{"BEGIN RSA PRIVATE KEY"},
		},
		{
			name:       "secret in session id",
			adapterID:  "codex",
			input:      `{"event":"PostToolUse","session_id":"ghp_0123456789abcdefghij"}`,
			wantErr:    "capture rejected",
			wantNoEcho: []string{"ghp_0123456789abcdefghij"},
		},
		{
			name:       "secret scan precedes event validation",
			adapterID:  "claude-code",
			input:      `{"prompt":"AKIAIOSFODNN7EXAMPLE"}`,
			wantErr:    "capture rejected",
			wantNoEcho: []string{"AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name: "secret in tool metadata",
			// The whole event body is scanned before normalization, so a
			// secret in the tool name is rejected at the input scan. The
			// narrower tool-only scan is defense in depth and is unreachable
			// through Capture.
			adapterID:  "claude-code",
			input:      `{"hook_event_name":"PostToolUse","tool_name":"AKIAIOSFODNN7EXAMPLE"}`,
			wantErr:    "capture rejected",
			wantNoEcho: []string{"AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name:      "negative source_seq",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"PostToolUse","source_seq":-1}`,
			wantErr:   "source_seq must be a non-negative integer",
		},
		{
			name:      "fractional source_seq",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"PostToolUse","source_seq":1.5}`,
			wantErr:   "source_seq must be a non-negative integer",
		},
		{
			name:      "oversized source_seq number",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"PostToolUse","source_seq":1e20}`,
			wantErr:   "source_seq must be a non-negative integer",
		},
		{
			name:      "bool source_seq",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"PostToolUse","source_seq":true}`,
			wantErr:   "source_seq must be a non-negative integer",
		},
		{
			name:      "non-numeric source_seq string",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"PostToolUse","source_seq":"abc"}`,
			wantErr:   "source_seq must be a non-negative integer",
		},
		{
			name:      "overflowing source_seq string",
			adapterID: "claude-code",
			input:     `{"hook_event_name":"PostToolUse","source_seq":"18446744073709551616"}`,
			wantErr:   "source_seq must be a non-negative integer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Capture(tc.adapterID, []byte(tc.input))
			if err == nil {
				t.Fatalf("Capture(%q, %s) = %+v, want error", tc.adapterID, tc.input, got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, CaptureResult{}) {
				t.Fatalf("failed Capture returned non-zero result: %+v", got)
			}
			for _, secret := range tc.wantNoEcho {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error echoed secret material: %q", err)
				}
			}
		})
	}
}

func TestUintField(t *testing.T) {
	cases := []struct {
		name    string
		m       map[string]any
		keys    []string
		want    uint64
		wantErr bool
	}{
		{"missing key", map[string]any{}, []string{"source_seq"}, 0, false},
		{"no keys", map[string]any{"source_seq": float64(1)}, nil, 0, false},
		{"falls through missing first key", map[string]any{"b": float64(3)}, []string{"a", "b"}, 3, false},
		{"float zero", map[string]any{"k": float64(0)}, []string{"k"}, 0, false},
		{"float integer", map[string]any{"k": float64(7)}, []string{"k"}, 7, false},
		{"float fractional", map[string]any{"k": 7.5}, []string{"k"}, 0, true},
		{"float negative", map[string]any{"k": float64(-1)}, []string{"k"}, 0, true},
		{"float too large", map[string]any{"k": 1e20}, []string{"k"}, 0, true},
		{"string integer", map[string]any{"k": "42"}, []string{"k"}, 42, false},
		{"string leading zeros", map[string]any{"k": "007"}, []string{"k"}, 7, false},
		{"string max uint64", map[string]any{"k": "18446744073709551615"}, []string{"k"}, math.MaxUint64, false},
		{"string empty", map[string]any{"k": ""}, []string{"k"}, 0, true},
		{"string negative", map[string]any{"k": "-1"}, []string{"k"}, 0, true},
		{"string garbage", map[string]any{"k": "7x"}, []string{"k"}, 0, true},
		{"bool", map[string]any{"k": true}, []string{"k"}, 0, true},
		{"int", map[string]any{"k": 5}, []string{"k"}, 0, true},
		{"nil value", map[string]any{"k": nil}, []string{"k"}, 0, true},
		{"slice value", map[string]any{"k": []any{}}, []string{"k"}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := uintField(tc.m, tc.keys...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("uintField(%v, %v) = %d, want error", tc.m, tc.keys, got)
				}
				if got != 0 {
					t.Fatalf("uintField returned %d on error, want 0", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("uintField(%v, %v): %v", tc.m, tc.keys, err)
			}
			if got != tc.want {
				t.Fatalf("uintField(%v, %v) = %d, want %d", tc.m, tc.keys, got, tc.want)
			}
		})
	}
}

func TestCaptureNormalizesAllowlistedMetadata(t *testing.T) {
	raw := `{
		"hook_event_name": "  PostToolUse  ",
		"session_id": "native-session-raw",
		"cwd": "/tmp/anchor-prompts/project",
		"tool_name": "  Bash  ",
		"timestamp": "2026-11-15T00:00:00Z",
		"source_event_id": "delivery-1",
		"source_seq": 7,
		"prompt": "must-not-be-copied",
		"tool_input": {"command": "echo fixture"},
		"transcript": "secret transcript body",
		"response": "assistant response body"
	}`
	got := mustCapture(t, "claude-code", raw)

	if got.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", got.SchemaVersion)
	}
	if got.Adapter != "claude-code" {
		t.Fatalf("Adapter = %q", got.Adapter)
	}
	if got.Event != "PostToolUse" {
		t.Fatalf("Event = %q, want trimmed PostToolUse", got.Event)
	}
	if got.Tool != "Bash" {
		t.Fatalf("Tool = %q, want trimmed Bash", got.Tool)
	}
	if got.Timestamp != "2026-11-15T00:00:00Z" {
		t.Fatalf("Timestamp = %q", got.Timestamp)
	}
	if want := shaRef("native-session-raw"); got.SessionRef != want {
		t.Fatalf("SessionRef = %q, want %q", got.SessionRef, want)
	}
	if want := shaRef("/tmp/anchor-prompts/project"); got.CwdRef != want {
		t.Fatalf("CwdRef = %q, want %q", got.CwdRef, want)
	}
	if want := shaRef("delivery-1"); got.SourceEventRef != want {
		t.Fatalf("SourceEventRef = %q, want %q", got.SourceEventRef, want)
	}
	if got.SourceSeq != 7 {
		t.Fatalf("SourceSeq = %d, want 7", got.SourceSeq)
	}
	if got.ContentPolicy != "metadata_only: prompt/tool_input/transcript omitted" {
		t.Fatalf("ContentPolicy = %q", got.ContentPolicy)
	}
	if got.NativeHookVerified {
		t.Fatal("NativeHookVerified must stay false")
	}
	if !got.DoesNotRestoreOrBlock {
		t.Fatal("DoesNotRestoreOrBlock must stay true")
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	for _, forbidden := range []string{
		"must-not-be-copied", "echo fixture", "secret transcript body",
		"assistant response body", "native-session-raw", "/tmp/anchor-prompts/project",
		"delivery-1",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("normalized capture leaked %q: %s", forbidden, encoded)
		}
	}

	if again := mustCapture(t, "claude-code", raw); !reflect.DeepEqual(got, again) {
		t.Fatalf("capture is not deterministic:\n%+v\n%+v", got, again)
	}
}

func TestCaptureFieldFallbacks(t *testing.T) {
	t.Run("event fallback", func(t *testing.T) {
		got := mustCapture(t, "codex", `{"event":"  post_tool_use  ","session_id":"s","tool":"shell"}`)
		if got.Adapter != "codex" || got.Event != "post_tool_use" || got.Tool != "shell" {
			t.Fatalf("unexpected capture: %+v", got)
		}
	})

	t.Run("hook_event_name wins over event", func(t *testing.T) {
		got := mustCapture(t, "claude-code", `{"hook_event_name":"PostToolUse","event":"ignored"}`)
		if got.Event != "PostToolUse" {
			t.Fatalf("Event = %q, want PostToolUse", got.Event)
		}
	})

	t.Run("tool_name wins over tool", func(t *testing.T) {
		got := mustCapture(t, "claude-code", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool":"ignored"}`)
		if got.Tool != "Bash" {
			t.Fatalf("Tool = %q, want Bash", got.Tool)
		}
	})

	t.Run("missing session id and cwd normalize to empty refs", func(t *testing.T) {
		got := mustCapture(t, "claude-code", `{"hook_event_name":"UserPromptSubmit"}`)
		if got.SessionRef != "" || got.CwdRef != "" || got.SourceEventRef != "" {
			t.Fatalf("expected empty refs, got %+v", got)
		}
		if got.Tool != "" || got.Timestamp != "" {
			t.Fatalf("expected empty metadata, got %+v", got)
		}
	})

	// Every hook event name documented for the three adapters must normalize
	// to itself; only SessionEnd is an end event.
	documentedEvents := map[string][]string{
		"claude-code": {"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "PreCompact", "SessionEnd"},
		"codex":       {"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "PreCompact", "SessionEnd"},
		"gemini-cli":  {"SessionStart", "BeforeAgent", "AfterAgent", "BeforeTool", "AfterTool", "PreCompress", "SessionEnd"},
	}
	for adapterID, events := range documentedEvents {
		for _, event := range events {
			t.Run(adapterID+"/"+event, func(t *testing.T) {
				raw := fmt.Sprintf(`{"event":%q,"session_id":"s"}`, event)
				got := mustCapture(t, adapterID, raw)
				if got.Adapter != adapterID || got.Event != event {
					t.Fatalf("capture = adapter %q event %q, want %q/%q", got.Adapter, got.Event, adapterID, event)
				}
				if want := event == "SessionEnd"; IsSessionEnd(got.Event) != want {
					t.Fatalf("IsSessionEnd(%q) != %v", got.Event, want)
				}
			})
		}
	}
}

func TestCaptureSourceIdentity(t *testing.T) {
	sessionRef := shaRef("s")
	seqFallback := shaRef(fmt.Sprintf("source-seq|%s|%s|%d", "claude-code", sessionRef, 7))
	cases := []struct {
		name    string
		input   string
		wantRef string
		wantSeq uint64
	}{
		{
			name:    "source_event_id",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","source_event_id":"delivery-a","source_seq":7}`,
			wantRef: shaRef("delivery-a"),
			wantSeq: 7,
		},
		{
			name:    "hook_event_id fallback",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","hook_event_id":"hook-a"}`,
			wantRef: shaRef("hook-a"),
		},
		{
			name:    "event_id fallback",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","event_id":"event-a"}`,
			wantRef: shaRef("event-a"),
		},
		{
			name:    "source_seq fallback",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","source_seq":7}`,
			wantRef: seqFallback,
			wantSeq: 7,
		},
		{
			name:    "source_seq fallback without session",
			input:   `{"hook_event_name":"PostToolUse","source_seq":7}`,
			wantRef: shaRef("source-seq|claude-code||7"),
			wantSeq: 7,
		},
		{
			name:    "no source identity",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s"}`,
			wantRef: "",
		},
		{
			name:    "explicit identity wins over sequence",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","source_event_id":"delivery-a","source_seq":9}`,
			wantRef: shaRef("delivery-a"),
			wantSeq: 9,
		},
		{
			name:    "numeric string sequence",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","source_seq":"7"}`,
			wantRef: seqFallback,
			wantSeq: 7,
		},
		{
			name:    "float sequence",
			input:   `{"hook_event_name":"PostToolUse","session_id":"s","source_seq":7.0}`,
			wantRef: seqFallback,
			wantSeq: 7,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustCapture(t, "claude-code", tc.input)
			if got.SourceEventRef != tc.wantRef {
				t.Fatalf("SourceEventRef = %q, want %q", got.SourceEventRef, tc.wantRef)
			}
			if got.SourceSeq != tc.wantSeq {
				t.Fatalf("SourceSeq = %d, want %d", got.SourceSeq, tc.wantSeq)
			}
			if tc.wantRef != "" && !strings.HasPrefix(tc.wantRef, "sha256:") {
				t.Fatalf("test expectation %q is not a sha256 ref", tc.wantRef)
			}
		})
	}
}

// The 1 MiB size cap (cmd/anchor maxCaptureInput) lives at the CLI I/O
// boundary; the library normalizer must never copy unknown fields, regardless
// of how large the supplied payload is.
func TestCaptureOmitsLargeUnknownFields(t *testing.T) {
	padding := strings.Repeat("x", 1<<20+64)
	raw := fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":"s","transcript":%q}`, padding)

	got, err := Capture("claude-code", []byte(raw))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("normalized capture unexpectedly large: %d bytes", len(encoded))
	}
	if strings.Contains(string(encoded), padding[:64]) {
		t.Fatal("normalized capture copied unknown transcript content")
	}
}

func TestIdempotencyKey(t *testing.T) {
	source := mustCapture(t, "claude-code", `{"hook_event_name":"PostToolUse","session_id":"s","source_event_id":"delivery-a","source_seq":7}`)

	key, err := IdempotencyKey("claude-code", "node-1", "fixture", source)
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if !strings.HasPrefix(key, "adapter_capture:") {
		t.Fatalf("key %q lacks adapter_capture: prefix", key)
	}
	hexPart := strings.TrimPrefix(key, "adapter_capture:")
	if len(hexPart) != 64 {
		t.Fatalf("key digest length = %d, want 64", len(hexPart))
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		t.Fatalf("key digest is not hex: %v", err)
	}
	if again, err := IdempotencyKey("claude-code", "node-1", "fixture", source); err != nil || again != key {
		t.Fatalf("same input produced key %q (err=%v), want %q", again, err, key)
	}

	variants := []struct {
		name      string
		adapterID string
		nodeID    string
		src       string
		result    CaptureResult
	}{
		{"different node id", "claude-code", "node-2", "fixture", source},
		{"different source", "claude-code", "node-1", "hook_unverified", source},
		{"different adapter", "codex", "node-1", "fixture", source},
		{"different session ref", "claude-code", "node-1", "fixture", func() CaptureResult { r := source; r.SessionRef = shaRef("other-session"); return r }()},
		{"different source event ref", "claude-code", "node-1", "fixture", func() CaptureResult { r := source; r.SourceEventRef = shaRef("other-event"); return r }()},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			got, err := IdempotencyKey(variant.adapterID, variant.nodeID, variant.src, variant.result)
			if err != nil {
				t.Fatalf("IdempotencyKey: %v", err)
			}
			if got == key {
				t.Fatalf("%s produced the same idempotency key %q", variant.name, got)
			}
		})
	}

	t.Run("mutable metadata and sequence are excluded", func(t *testing.T) {
		mutated := source
		mutated.Event = "Stop"
		mutated.Tool = "Read"
		mutated.Timestamp = "2030-01-01T00:00:00Z"
		mutated.CwdRef = shaRef("/elsewhere")
		mutated.SourceSeq = 99
		mutated.SchemaVersion = 99
		got, err := IdempotencyKey("claude-code", "node-1", "fixture", mutated)
		if err != nil {
			t.Fatalf("IdempotencyKey: %v", err)
		}
		if got != key {
			t.Fatalf("mutable metadata changed the key: %q vs %q", got, key)
		}
	})

	t.Run("missing source identity is still total", func(t *testing.T) {
		// The CLI (cmd/anchor) rejects captures without a stable source
		// identity before deriving the key; the key function itself never
		// errors for string payloads and yields a deterministic key.
		empty := CaptureResult{SessionRef: shaRef("s")}
		got, err := IdempotencyKey("claude-code", "node-1", "fixture", empty)
		if err != nil {
			t.Fatalf("IdempotencyKey: %v", err)
		}
		if got == key {
			t.Fatal("capture without source identity reused the identity-bearing key")
		}
		if again, _ := IdempotencyKey("claude-code", "node-1", "fixture", empty); again != got {
			t.Fatalf("key without source identity is not deterministic: %q vs %q", got, again)
		}
	})
}

func TestRenderAdapters(t *testing.T) {
	cases := []struct {
		id         string
		nativeFile string
	}{
		{"claude-code", "settings.fragment.json"},
		{"codex", "config.toml.fragment"},
		{"gemini-cli", "settings.fragment.json"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			capability, err := Get(tc.id)
			if err != nil {
				t.Fatalf("Get(%q): %v", tc.id, err)
			}
			out, files, err := Render(tc.id)
			if err != nil {
				t.Fatalf("Render(%q): %v", tc.id, err)
			}
			wantCmd := "anchor adapter capture --adapter " + tc.id + " --stdin --print"
			if out.SchemaVersion != 1 {
				t.Fatalf("SchemaVersion = %d, want 1", out.SchemaVersion)
			}
			if !reflect.DeepEqual(out.Adapter, capability) {
				t.Fatalf("rendered adapter mismatch:\ngot  %+v\nwant %+v", out.Adapter, capability)
			}
			if out.HookCommand != wantCmd {
				t.Fatalf("HookCommand = %q, want %q", out.HookCommand, wantCmd)
			}
			if out.NativeConfigFile != tc.nativeFile {
				t.Fatalf("NativeConfigFile = %q, want %q", out.NativeConfigFile, tc.nativeFile)
			}
			if out.Installation != "manual_review_only" {
				t.Fatalf("Installation = %q", out.Installation)
			}
			if !out.DoesNotOverwriteUserConfig || !out.DoesNotRestoreOrBlock {
				t.Fatalf("render overclaims behavior: %+v", out)
			}
			if len(files) != 2 {
				t.Fatalf("rendered %d files, want 2", len(files))
			}
			if files[0].Path != "anchor-adapter.json" || files[1].Path != tc.nativeFile {
				t.Fatalf("unexpected rendered files: %q, %q", files[0].Path, files[1].Path)
			}
			if string(files[1].Content) != out.NativeConfig {
				t.Fatal("rendered native file does not match NativeConfig")
			}
			if !strings.Contains(out.NativeConfig, "UNVERIFIED") {
				t.Fatalf("native config is missing the UNVERIFIED status: %q", out.NativeConfig)
			}

			if !strings.HasSuffix(string(files[0].Content), "\n") {
				t.Fatal("manifest does not end with a newline")
			}
			manifest := decodeJSONObject(t, files[0].Content)
			if manifest["schema_version"] != float64(1) {
				t.Fatalf("manifest schema_version = %v", manifest["schema_version"])
			}
			manifestAdapter := jsonObject(t, manifest["adapter"])
			if manifestAdapter["id"] != tc.id {
				t.Fatalf("manifest adapter id = %v, want %q", manifestAdapter["id"], tc.id)
			}
			if manifest["hook_command"] != wantCmd {
				t.Fatalf("manifest hook_command = %v", manifest["hook_command"])
			}
			if manifest["native_config_file"] != tc.nativeFile {
				t.Fatalf("manifest native_config_file = %v", manifest["native_config_file"])
			}
			if manifest["native_config"] != out.NativeConfig {
				t.Fatal("manifest native_config does not match NativeConfig")
			}
			if manifest["installation"] != "manual_review_only" {
				t.Fatalf("manifest installation = %v", manifest["installation"])
			}
			if manifest["does_not_overwrite_user_config"] != true || manifest["does_not_restore_or_block"] != true {
				t.Fatalf("manifest flags overclaim: %v", manifest)
			}

			assertNativeConfigShape(t, tc.id, files[1].Content, wantCmd)

			out2, files2, err := Render(tc.id)
			if err != nil {
				t.Fatalf("second Render: %v", err)
			}
			if !reflect.DeepEqual(out, out2) || len(files) != len(files2) {
				t.Fatal("Render is not deterministic")
			}
			for i := range files {
				if files[i].Path != files2[i].Path || !bytes.Equal(files[i].Content, files2[i].Content) {
					t.Fatalf("Render file %d is not deterministic", i)
				}
			}
		})
	}
}

func assertNativeConfigShape(t *testing.T, adapterID string, content []byte, wantCmd string) {
	t.Helper()
	switch adapterID {
	case "claude-code":
		cfg := decodeJSONObject(t, content)
		status, _ := cfg["_anchor_status"].(string)
		if !strings.Contains(status, "UNVERIFIED") {
			t.Fatalf("claude _anchor_status = %q", status)
		}
		hooks := jsonObject(t, cfg["hooks"])
		entries := jsonArray(t, hooks["PostToolUse"])
		if len(entries) != 1 {
			t.Fatalf("claude hooks.PostToolUse has %d entries, want 1", len(entries))
		}
		entry := jsonObject(t, entries[0])
		if entry["matcher"] != "*" {
			t.Fatalf("claude matcher = %v, want *", entry["matcher"])
		}
		commands := jsonArray(t, entry["hooks"])
		command := jsonObject(t, commands[0])
		if command["type"] != "command" || command["command"] != wantCmd {
			t.Fatalf("claude hook = %v, want command %q", command, wantCmd)
		}
	case "gemini-cli":
		cfg := decodeJSONObject(t, content)
		hooks := jsonObject(t, cfg["hooks"])
		entries := jsonArray(t, hooks["PostToolUse"])
		entry := jsonObject(t, entries[0])
		if entry["command"] != wantCmd {
			t.Fatalf("gemini hook command = %v, want %q", entry["command"], wantCmd)
		}
	case "codex":
		text := string(content)
		for _, want := range []string{"# ANCHOR STATUS: UNVERIFIED", "[hooks]", fmt.Sprintf("post_tool_use = %q", wantCmd)} {
			if !strings.Contains(text, want) {
				t.Fatalf("codex config missing %q:\n%s", want, text)
			}
		}
	default:
		t.Fatalf("unexpected adapter id %q", adapterID)
	}
}

func TestRenderUnknownAdapter(t *testing.T) {
	for _, id := range []string{"", "nope", "Claude-Code", "claude_code"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			out, files, err := Render(id)
			if err == nil {
				t.Fatalf("Render(%q) = %+v, %v; want unknown adapter error", id, out, files)
			}
			if !strings.Contains(err.Error(), "unknown adapter") {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(out, RenderOutput{}) || files != nil {
				t.Fatalf("Render(%q) returned output alongside error: %+v, %v", id, out, files)
			}
		})
	}
}

func TestNativeConfigUnknownAdapter(t *testing.T) {
	name, content, err := nativeConfig(Capability{ID: "unregistered"})
	if err == nil {
		t.Fatalf("nativeConfig accepted an unknown adapter: %q %q", name, content)
	}
	if !strings.Contains(err.Error(), "unknown adapter") {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "" || content != "" {
		t.Fatalf("nativeConfig returned %q/%q alongside error", name, content)
	}

	for _, id := range []string{"claude-code", "codex", "gemini-cli"} {
		capability, err := Get(id)
		if err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
		name, content, err := nativeConfig(capability)
		if err != nil {
			t.Fatalf("nativeConfig(%q): %v", id, err)
		}
		if name == "" || content == "" {
			t.Fatalf("nativeConfig(%q) returned empty fragment", id)
		}
		if !strings.Contains(content, hookCommand(id)) {
			t.Fatalf("nativeConfig(%q) does not reference %q", id, hookCommand(id))
		}
	}
}

func TestGetAndAll(t *testing.T) {
	wantIDs := []string{"claude-code", "codex", "gemini-cli"}
	all := All()
	if len(all) != len(wantIDs) {
		t.Fatalf("All() returned %d adapters, want %d", len(all), len(wantIDs))
	}
	for i, id := range wantIDs {
		if all[i].ID != id {
			t.Fatalf("All()[%d].ID = %q, want %q (sorted by id)", i, all[i].ID, id)
		}
		got, err := Get(id)
		if err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
		if !reflect.DeepEqual(got, all[i]) {
			t.Fatalf("Get(%q) = %+v, want %+v", id, got, all[i])
		}
		if got.Name == "" || got.EvidenceGrade != "D" || got.NativeHookStatus != "unverified" || got.EndToEndVerified {
			t.Fatalf("unexpected capability metadata for %q: %+v", id, got)
		}
		if len(got.ConfigTargets) == 0 || len(got.CaptureFields) == 0 || len(got.Features) == 0 || len(got.NotClaimed) == 0 {
			t.Fatalf("capability %q is missing metadata: %+v", id, got)
		}
	}

	for _, id := range []string{"", "nope", "Claude-Code", "claude_code", "gemini"} {
		t.Run("unknown/"+id, func(t *testing.T) {
			got, err := Get(id)
			if err == nil {
				t.Fatalf("Get(%q) = %+v, want error", id, got)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("unknown adapter %q", id)) {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, Capability{}) {
				t.Fatalf("Get(%q) returned a capability alongside error: %+v", id, got)
			}
		})
	}
}

func TestWriteCreatesFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, id := range []string{"claude-code", "codex", "gemini-cli"} {
		t.Run(id, func(t *testing.T) {
			target := t.TempDir()
			out, paths, err := Write(id, target)
			if err != nil {
				t.Fatalf("Write(%q): %v", id, err)
			}
			wantOut, wantFiles, err := Render(id)
			if err != nil {
				t.Fatalf("Render(%q): %v", id, err)
			}
			if !reflect.DeepEqual(out, wantOut) {
				t.Fatalf("Write output = %+v, want %+v", out, wantOut)
			}
			if len(paths) != 2 || len(wantFiles) != 2 {
				t.Fatalf("wrote %d paths, want 2", len(paths))
			}
			for i, p := range paths {
				if filepath.Base(p) != wantFiles[i].Path {
					t.Fatalf("path %q does not match rendered file %q", p, wantFiles[i].Path)
				}
				info, err := os.Lstat(p)
				if err != nil {
					t.Fatalf("Lstat(%q): %v", p, err)
				}
				if !info.Mode().IsRegular() {
					t.Fatalf("%q is not a regular file", p)
				}
				if perm := info.Mode().Perm(); perm != 0o600 {
					t.Fatalf("%q permissions = %o, want 600", p, perm)
				}
				content, err := os.ReadFile(p)
				if err != nil {
					t.Fatalf("ReadFile(%q): %v", p, err)
				}
				if !bytes.Equal(content, wantFiles[i].Content) {
					t.Fatalf("%q content does not match Render output", p)
				}
			}
			dirInfo, err := os.Stat(filepath.Join(target, id))
			if err != nil {
				t.Fatalf("Stat adapter dir: %v", err)
			}
			if !dirInfo.IsDir() {
				t.Fatalf("adapter target %q is not a directory", dirInfo.Name())
			}
			if perm := dirInfo.Mode().Perm(); perm != 0o700 {
				t.Fatalf("adapter dir permissions = %o, want 700", perm)
			}

			if _, _, err := Write(id, target); err == nil {
				t.Fatal("second Write overwrote existing rendered files")
			} else if !strings.Contains(err.Error(), "refusing to overwrite") {
				t.Fatalf("unexpected overwrite error: %v", err)
			}
			for i, p := range paths {
				content, err := os.ReadFile(p)
				if err != nil {
					t.Fatalf("ReadFile after refused overwrite: %v", err)
				}
				if !bytes.Equal(content, wantFiles[i].Content) {
					t.Fatalf("%q changed after refused overwrite", p)
				}
			}
		})
	}
}

func TestWriteTargetEdgeCases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("empty target", func(t *testing.T) {
		for _, target := range []string{"", "   ", "\t\n"} {
			if _, _, err := Write("claude-code", target); err == nil {
				t.Fatalf("Write accepted empty target %q", target)
			} else if !strings.Contains(err.Error(), "--target is required") {
				t.Fatalf("unexpected empty-target error: %v", err)
			}
		}
	})

	t.Run("unknown adapter", func(t *testing.T) {
		if _, _, err := Write("nope", t.TempDir()); err == nil {
			t.Fatal("Write accepted an unknown adapter")
		} else if !strings.Contains(err.Error(), "unknown adapter") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("target is a regular file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Write("claude-code", file); err == nil {
			t.Fatal("Write accepted a regular file as target")
		}
	})

	t.Run("nested target is created", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "nested", "deeper", "out")
		_, paths, err := Write("codex", target)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		for _, p := range paths {
			if _, err := os.Lstat(p); err != nil {
				t.Fatalf("rendered file missing: %v", err)
			}
		}
	})

	t.Run("symlink to a safe directory is resolved and allowed", func(t *testing.T) {
		safe := t.TempDir()
		link := filepath.Join(t.TempDir(), "safe-link")
		if err := os.Symlink(safe, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, paths, err := Write("gemini-cli", link)
		if err != nil {
			t.Fatalf("Write via symlink to safe dir: %v", err)
		}
		for _, p := range paths {
			rel, err := filepath.Rel(link, p)
			if err != nil {
				t.Fatalf("Rel: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(safe, rel)); err != nil {
				t.Fatalf("rendered file missing in symlink target: %v", err)
			}
		}
	})

	t.Run("adapter subdirectory symlink is refused", func(t *testing.T) {
		target := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(target, "claude-code")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := Write("claude-code", target); err == nil {
			t.Fatal("Write accepted a symlinked adapter subdirectory")
		} else if !strings.Contains(err.Error(), "must be a real directory") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("adapter subdirectory that is a file", func(t *testing.T) {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "claude-code"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Write("claude-code", target); err == nil {
			t.Fatal("Write accepted a file in place of the adapter subdirectory")
		}
	})

	t.Run("symlink loop in target path", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), "loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := Write("claude-code", filepath.Join(loop, "child")); err == nil {
			t.Fatal("Write accepted a target containing a symlink loop")
		}
	})
}

func TestWriteRefusesSharedConfigPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sharedRels := []string{
		".claude", ".codex", ".gemini",
		filepath.Join(".config", "claude"),
		filepath.Join(".config", "codex"),
		filepath.Join(".config", "gemini"),
	}
	for _, rel := range sharedRels {
		t.Run("shared/"+filepath.ToSlash(rel), func(t *testing.T) {
			for _, target := range []string{
				filepath.Join(home, rel),
				filepath.Join(home, rel, "nested", "output"),
			} {
				if _, _, err := Write("claude-code", target); err == nil {
					t.Fatalf("Write accepted shared config target %q", target)
				} else if !strings.Contains(err.Error(), "shared user CLI config") {
					t.Fatalf("unexpected shared-config error: %v", err)
				}
			}
		})
	}

	t.Run("symlink into shared config", func(t *testing.T) {
		shared := filepath.Join(home, ".claude")
		if err := os.MkdirAll(shared, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "alias-to-claude")
		if err := os.Symlink(shared, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, _, err := Write("claude-code", link); err == nil {
			t.Fatal("Write accepted a symlink into shared user config")
		} else if !strings.Contains(err.Error(), "shared user CLI config") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// HOME itself can be reached through a symlink (on macOS /var is a symlink to
// /private/var). Both Write and pathguard must canonicalize the target and HOME
// before comparing, so ~/.claude is refused through either name while an
// ordinary directory under the same HOME stays writable.
func TestWriteViaSymlinkedHome(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "real-home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	linkHome := filepath.Join(root, "link-home")
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("HOME", linkHome)

	for _, rel := range []string{".claude", ".codex", filepath.Join(".config", "gemini")} {
		for _, base := range []string{linkHome, realHome} {
			if _, _, err := Write("claude-code", filepath.Join(base, rel)); err == nil {
				t.Fatalf("Write accepted shared %q through HOME %q", rel, base)
			}
		}
	}
	if _, paths, err := Write("claude-code", filepath.Join(realHome, "scratch")); err != nil {
		t.Fatalf("Write to safe target under symlinked HOME: %v", err)
	} else if len(paths) != 2 {
		t.Fatalf("wrote %d files, want 2", len(paths))
	}
}

// Rendered files are created with O_EXCL; the preflight and creation loops
// must also surface filesystem permission failures rather than silently
// reporting success.
func TestWritePermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("adapter directory is not searchable", func(t *testing.T) {
		target := t.TempDir()
		dir := filepath.Join(target, "claude-code")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if _, _, err := Write("claude-code", target); err == nil {
			t.Fatal("Write succeeded inside an unsearchable adapter directory")
		}
	})

	t.Run("adapter directory is not writable", func(t *testing.T) {
		target := t.TempDir()
		dir := filepath.Join(target, "claude-code")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if _, _, err := Write("claude-code", target); err == nil {
			t.Fatal("Write succeeded inside a read-only adapter directory")
		}
	})
}
