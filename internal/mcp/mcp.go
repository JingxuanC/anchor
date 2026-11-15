// Package mcp implements a minimal, dependency-free MCP (Model Context
// Protocol) stdio server that exposes read-only Anchor workspace tools.
//
// The transport is newline-delimited JSON-RPC 2.0: exactly one JSON object per
// line, no Content-Length framing. Messages are processed sequentially and every
// response is flushed before the next line is read. Notifications (messages
// without an id, and methods starting with "notifications/") never receive a
// response. Malformed JSON lines produce a -32700 parse error with a null id and
// do not stop the server.
//
// Every tool is read-only: the handlers open the workspace and replay its event
// log, but they never append events, create artifacts, record handoff
// acknowledgements, or otherwise write workspace files.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "anchor"
	serverVersion   = "0.1.0"
)

// JSON-RPC 2.0 error codes used by the server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Serve runs an MCP stdio server exposing read-only Anchor tools for
// workspaceDir. It reads newline-delimited JSON-RPC 2.0 messages from in and
// writes responses to out.
//
// Serve returns nil when in reaches EOF, ctx.Err() when ctx is cancelled, and a
// non-nil error only when the input stream fails or a response cannot be
// written.
func Serve(ctx context.Context, workspaceDir string, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return fmt.Errorf("mcp: in and out must be non-nil")
	}
	s := &server{dir: workspaceDir, ctx: ctx, out: bufio.NewWriter(out)}
	lines := make(chan readItem, 64)
	go readLines(ctx, in, lines)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-lines:
			if !ok {
				return nil
			}
			if len(item.line) > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := s.handleLine(item.line); err != nil {
					return err
				}
			}
			if item.err != nil {
				if errors.Is(item.err, io.EOF) {
					return nil
				}
				return item.err
			}
		}
	}
}

// readItem is one line (without the trailing newline) or a terminal read error.
type readItem struct {
	line []byte
	err  error
}

// readLines splits the input into lines on '\n' without imposing a fixed token
// limit. A final line without a trailing newline is still delivered.
func readLines(ctx context.Context, in io.Reader, ch chan<- readItem) {
	defer close(ch)
	br := bufio.NewReader(in)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			select {
			case ch <- readItem{line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case ch <- readItem{err: err}:
			case <-ctx.Done():
			}
			return
		}
	}
}

type server struct {
	dir string
	ctx context.Context
	out *bufio.Writer
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleLine decodes and dispatches one line of input. Blank lines are ignored.
func (s *server) handleLine(raw []byte) error {
	line := bytes.TrimSpace(raw)
	if len(line) == 0 {
		return nil
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return s.writeError(json.RawMessage("null"), codeParseError, "Parse error")
	}
	// A message without an id field is a notification; a JSON null id is an
	// (unusual but legal) request id that must be echoed back as null.
	notification := len(req.ID) == 0
	respID := req.ID
	if notification {
		respID = json.RawMessage("null")
	}
	if req.Method == "" {
		if notification {
			return nil
		}
		return s.writeError(respID, codeInvalidRequest, "Invalid Request")
	}
	// MCP initialization notification and any future notifications/* method
	// must not produce a response, even if a client wrongly attaches an id.
	if strings.HasPrefix(req.Method, "notifications/") {
		return nil
	}
	result, rpcErr := s.dispatch(req)
	if notification {
		// Notifications never get a response, even for unknown methods.
		return nil
	}
	if rpcErr != nil {
		return s.writeError(respID, rpcErr.Code, rpcErr.Message)
	}
	return s.writeResult(respID, result)
}

func (s *server) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return initializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities:    capabilities{Tools: toolsCapability{ListChanged: false}},
			ServerInfo:      serverInfo{Name: serverName, Version: serverVersion},
		}, nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return toolsListResult{Tools: toolList()}, nil
	case "tools/call":
		return s.callTool(req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "Method not found: " + req.Method}
	}
}

func (s *server) writeResult(id json.RawMessage, result any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return s.writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return s.writeResponse(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *server) writeResponse(resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := s.out.Write(b); err != nil {
		return err
	}
	if err := s.out.WriteByte('\n'); err != nil {
		return err
	}
	return s.out.Flush()
}

// initializeResult is the MCP initialization response.
type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

type capabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
