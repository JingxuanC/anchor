package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// runServer drives Serve to completion with a bytes-buffer client: the whole
// input is written first and all newline-delimited output lines are collected.
func runServer(t *testing.T, workspaceDir, input string) ([]string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Serve(context.Background(), workspaceDir, strings.NewReader(input), &out)
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, err
}

// mustRunServer runs Serve and decodes every response line into a JSON object.
func mustRunServer(t *testing.T, workspaceDir, input string) []map[string]any {
	t.Helper()
	lines, err := runServer(t, workspaceDir, input)
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	msgs := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response line is not JSON: %v: %q", err, line)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func oneResponse(t *testing.T, workspaceDir, input string) map[string]any {
	t.Helper()
	msgs := mustRunServer(t, workspaceDir, input)
	if len(msgs) != 1 {
		t.Fatalf("want exactly one response, got %d: %v", len(msgs), msgs)
	}
	return msgs[0]
}

// rpcLine builds one newline-delimited JSON-RPC message. A nil id means the
// message is a notification (no "id" field at all).
func rpcLine(t *testing.T, id any, method string, params any) string {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b) + "\n"
}

func assertResponseID(t *testing.T, resp map[string]any, want float64) {
	t.Helper()
	got, ok := resp["id"].(float64)
	if !ok {
		t.Fatalf("response has no numeric id: %v", resp)
	}
	if got != want {
		t.Fatalf("response id = %v, want %v", got, want)
	}
}

func responseError(t *testing.T, resp map[string]any) (float64, string) {
	t.Helper()
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %v", resp)
	}
	code, ok := errObj["code"].(float64)
	if !ok {
		t.Fatalf("error has no numeric code: %v", errObj)
	}
	msg, _ := errObj["message"].(string)
	return code, msg
}

func TestServeInitializeResponseShape(t *testing.T) {
	resp := oneResponse(t, t.TempDir(), rpcLine(t, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
	}))
	if resp["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc = %v", resp["jsonrpc"])
	}
	assertResponseID(t, resp, 1)
	if _, hasError := resp["error"]; hasError {
		t.Fatalf("initialize returned an error: %v", resp)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result is not an object: %v", resp)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing: %v", result)
	}
	tools, ok := caps["tools"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.tools missing: %v", caps)
	}
	if changed, ok := tools["listChanged"].(bool); !ok || changed {
		t.Fatalf("tools.listChanged = %v, want false", tools["listChanged"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo missing: %v", result)
	}
	if info["name"] != "anchor" || info["version"] != "0.1.0" {
		t.Fatalf("serverInfo = %v", info)
	}
}

func TestServeToolsList(t *testing.T) {
	resp := oneResponse(t, t.TempDir(), rpcLine(t, 2, "tools/list", nil))
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result is not an object: %v", resp)
	}
	raw, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result.tools is not an array: %v", result)
	}
	if len(raw) != 4 {
		t.Fatalf("want exactly 4 tools, got %d", len(raw))
	}
	wantNames := map[string]bool{
		"anchor_status":          true,
		"anchor_tree":            true,
		"anchor_evidence_verify": true,
		"anchor_restore_plan":    true,
	}
	seen := map[string]bool{}
	for _, entry := range raw {
		tool, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %v", entry)
		}
		name, _ := tool["name"].(string)
		if !wantNames[name] {
			t.Fatalf("unexpected tool %q", name)
		}
		if seen[name] {
			t.Fatalf("duplicate tool %q", name)
		}
		seen[name] = true
		if desc, _ := tool["description"].(string); desc == "" {
			t.Fatalf("tool %q has no description", name)
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q has no inputSchema object", name)
		}
		if schema["type"] != "object" {
			t.Fatalf("tool %q inputSchema.type = %v", name, schema["type"])
		}
		if ap, ok := schema["additionalProperties"].(bool); !ok || ap {
			t.Fatalf("tool %q additionalProperties = %v, want false", name, schema["additionalProperties"])
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("tool %q inputSchema.properties missing", name)
		}
		required, _ := schema["required"].([]any)
		switch name {
		case "anchor_evidence_verify":
			if len(required) != 1 || required[0] != "evidence_id" {
				t.Fatalf("anchor_evidence_verify required = %v", required)
			}
			prop, _ := props["evidence_id"].(map[string]any)
			if prop["type"] != "string" {
				t.Fatalf("evidence_id schema = %v", props["evidence_id"])
			}
		case "anchor_status":
			prop, _ := props["repo"].(map[string]any)
			if prop["type"] != "string" {
				t.Fatalf("anchor_status repo schema = %v", props["repo"])
			}
		case "anchor_tree":
			if prop, _ := props["include_deleted"].(map[string]any); prop["type"] != "boolean" {
				t.Fatalf("include_deleted schema = %v", props["include_deleted"])
			}
			limit, _ := props["limit"].(map[string]any)
			if limit["type"] != "integer" {
				t.Fatalf("limit schema = %v", limit)
			}
			if min, ok := limit["minimum"].(float64); !ok || min != 1 {
				t.Fatalf("limit minimum = %v", limit["minimum"])
			}
			if max, ok := limit["maximum"].(float64); !ok || max != 1000 {
				t.Fatalf("limit maximum = %v", limit["maximum"])
			}
		case "anchor_restore_plan":
			for _, key := range []string{"packet", "repo"} {
				prop, _ := props[key].(map[string]any)
				if prop["type"] != "string" {
					t.Fatalf("anchor_restore_plan %s schema = %v", key, props[key])
				}
			}
		}
	}
	for name := range wantNames {
		if !seen[name] {
			t.Fatalf("tools/list is missing %q", name)
		}
	}
}

func TestServePingNotificationsAndBlankLines(t *testing.T) {
	input := "\n" +
		rpcLine(t, nil, "notifications/initialized", nil) +
		rpcLine(t, nil, "notifications/cancelled", map[string]any{"requestId": 1}) +
		rpcLine(t, 6, "notifications/initialized", nil) + // notifications never answer, even with an id
		"   \n" +
		rpcLine(t, 5, "ping", nil) +
		rpcLine(t, nil, "does/not/exist", nil) + // unknown notification: no response
		"\n"
	resp := oneResponse(t, t.TempDir(), input)
	assertResponseID(t, resp, 5)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("ping result is not an object: %v", resp)
	}
	if len(result) != 0 {
		t.Fatalf("ping result = %v, want {}", result)
	}
}

func TestServeUnknownMethod(t *testing.T) {
	resp := oneResponse(t, t.TempDir(), rpcLine(t, 3, "resources/list", nil))
	assertResponseID(t, resp, 3)
	if _, hasResult := resp["result"]; hasResult {
		t.Fatalf("unknown method must not carry a result: %v", resp)
	}
	code, msg := responseError(t, resp)
	if code != codeMethodNotFound {
		t.Fatalf("error code = %v, want %d (%s)", code, codeMethodNotFound, msg)
	}
}

func TestServeMalformedJSONDoesNotKillServer(t *testing.T) {
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\n" +
		"this is not json\n" +
		rpcLine(t, 2, "ping", nil)
	msgs := mustRunServer(t, t.TempDir(), input)
	if len(msgs) != 3 {
		t.Fatalf("want 3 responses (2 parse errors + ping), got %d: %v", len(msgs), msgs)
	}
	for i := 0; i < 2; i++ {
		msg := msgs[i]
		if _, ok := msg["id"]; !ok {
			t.Fatalf("parse error response %d has no id key: %v", i, msg)
		}
		if msg["id"] != nil {
			t.Fatalf("parse error response id = %v, want null", msg["id"])
		}
		code, _ := responseError(t, msg)
		if code != codeParseError {
			t.Fatalf("parse error code = %v, want %d", code, codeParseError)
		}
	}
	assertResponseID(t, msgs[2], 2)
	if _, ok := msgs[2]["result"].(map[string]any); !ok {
		t.Fatalf("server did not continue after malformed lines: %v", msgs[2])
	}
}

func TestServeToolsCallInvalidParams(t *testing.T) {
	input := rpcLine(t, 10, "tools/call", map[string]any{"arguments": map[string]any{}}) + // missing name
		rpcLine(t, 11, "tools/call", map[string]any{"name": "anchor_nope", "arguments": map[string]any{}}) + // unknown tool
		rpcLine(t, nil, "tools/call", map[string]any{"name": "anchor_nope"}) + // notification: no response
		rpcLine(t, 12, "tools/call", map[string]any{"name": "anchor_tree", "arguments": []any{"wrong"}})
	msgs := mustRunServer(t, t.TempDir(), input)
	if len(msgs) != 3 {
		t.Fatalf("want 3 responses, got %d: %v", len(msgs), msgs)
	}
	for _, want := range []float64{10, 11} {
		var found bool
		for _, msg := range msgs {
			if id, ok := msg["id"].(float64); ok && id == want {
				found = true
				code, _ := responseError(t, msg)
				if code != codeInvalidParams {
					t.Fatalf("tools/call id %v error code = %v, want %d", want, code, codeInvalidParams)
				}
			}
		}
		if !found {
			t.Fatalf("missing response for id %v: %v", want, msgs)
		}
	}
	// Bad argument shapes are tool failures, not protocol errors.
	var badArgs map[string]any
	for _, msg := range msgs {
		if id, ok := msg["id"].(float64); ok && id == 12 {
			badArgs = msg
		}
	}
	if badArgs == nil {
		t.Fatalf("missing response for id 12: %v", msgs)
	}
	result, ok := badArgs["result"].(map[string]any)
	if !ok {
		t.Fatalf("bad arguments must produce a tool result: %v", badArgs)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("bad arguments result isError = %v, want true", result["isError"])
	}
}

func TestServeCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Serve(ctx, t.TempDir(), strings.NewReader(""), io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve = %v, want context.Canceled", err)
	}
}

// TestServeFlushesAndStopsOnCancel uses io.Pipe as a real interactive client: it
// proves a response is flushed before the input reaches EOF, and that cancelling
// ctx ends Serve even while the read loop is blocked waiting for input.
func TestServeFlushesAndStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := Serve(ctx, dir, inR, outW)
		_ = outW.Close()
		done <- err
	}()
	t.Cleanup(func() { _ = inW.Close() })

	if _, err := io.WriteString(inW, rpcLine(t, 1, "ping", nil)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line := readLineWithin(t, bufio.NewReader(outR), 5*time.Second)
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("response is not JSON: %v: %q", err, line)
	}
	assertResponseID(t, resp, 1)
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("ping did not answer before input EOF: %v", resp)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

// readLineWithin reads one newline-terminated line, failing the test instead of
// hanging forever if the server never responds.
func readLineWithin(t *testing.T, r *bufio.Reader, timeout time.Duration) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		done <- result{line: line, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read response: %v", got.err)
		}
		return got.line
	case <-time.After(timeout):
		t.Fatal("timed out waiting for server response")
		return ""
	}
}

func TestServeRejectsNilStreams(t *testing.T) {
	if err := Serve(context.Background(), t.TempDir(), nil, io.Discard); err == nil {
		t.Fatal("Serve(nil in) must fail")
	}
	if err := Serve(context.Background(), t.TempDir(), strings.NewReader(""), nil); err == nil {
		t.Fatal("Serve(nil out) must fail")
	}
}

func TestServeReadErrorIsReturned(t *testing.T) {
	err := Serve(context.Background(), t.TempDir(), failingReader{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Serve = %v, want the read error", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestServeRequestWithNullIDIsAnswered(t *testing.T) {
	// "id": null is a request (not a notification) and must be echoed as null.
	resp := oneResponse(t, t.TempDir(), "{\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"ping\"}\n")
	if _, ok := resp["id"]; !ok {
		t.Fatalf("response has no id key: %v", resp)
	}
	if resp["id"] != nil {
		t.Fatalf("response id = %v, want null", resp["id"])
	}
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("ping result missing: %v", resp)
	}
}

func TestServeInvalidRequestObject(t *testing.T) {
	// A JSON object with an id but no method is an invalid request, not a
	// notification, and must receive -32600.
	resp := oneResponse(t, t.TempDir(), "{\"jsonrpc\":\"2.0\",\"id\":7}\n")
	assertResponseID(t, resp, 7)
	code, _ := responseError(t, resp)
	if code != codeInvalidRequest {
		t.Fatalf("error code = %v, want %d", code, codeInvalidRequest)
	}
	// The same object without an id is a notification and must stay silent.
	msgs := mustRunServer(t, t.TempDir(), "{\"jsonrpc\":\"2.0\"}\n")
	if len(msgs) != 0 {
		t.Fatalf("invalid notification produced a response: %v", msgs)
	}
}
