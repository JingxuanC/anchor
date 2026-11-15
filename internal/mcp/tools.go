package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

const (
	defaultTreeLimit = 200
	maxTreeLimit     = 1000
)

// toolDef is one entry of the tools/list result.
type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type                 string                    `json:"type"`
	Properties           map[string]propertySchema `json:"properties"`
	Required             []string                  `json:"required,omitempty"`
	AdditionalProperties bool                      `json:"additionalProperties"`
}

type propertySchema struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Minimum     *int   `json:"minimum,omitempty"`
	Maximum     *int   `json:"maximum,omitempty"`
}

func intPtr(v int) *int { return &v }

// anchorToolList is the fixed, ordered tool catalog exposed by tools/list.
var anchorToolList = []toolDef{
	{
		Name:        "anchor_status",
		Description: "Read-only Anchor workspace status: workspace identity, projection and recovery status, the successor handoff gate, event replay counts, and tree counts. Pass repo to also evaluate recovery against a git working tree.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propertySchema{
				"repo": {Type: "string", Description: "git working tree used for recovery evaluation (optional)"},
			},
			AdditionalProperties: false,
		},
	},
	{
		Name:        "anchor_tree",
		Description: "Read-only projection of the Anchor tree: nodes (optionally including tombstoned nodes), registered evidence entries, and adapter session ids.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propertySchema{
				"include_deleted": {Type: "boolean", Description: "include tombstoned nodes (default false)"},
				"limit":           {Type: "integer", Description: "maximum number of nodes to return (default 200, max 1000)", Minimum: intPtr(1), Maximum: intPtr(maxTreeLimit)},
			},
			AdditionalProperties: false,
		},
	},
	{
		Name:        "anchor_evidence_verify",
		Description: "Verify that a registered evidence artifact still exists, matches its recorded sha256 and size, and contains no rejected secret material. Read-only; never mutates the artifact.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propertySchema{
				"evidence_id": {Type: "string", Description: "evidence id recorded in the workspace replay"},
			},
			Required:             []string{"evidence_id"},
			AdditionalProperties: false,
		},
	},
	{
		Name:        "anchor_restore_plan",
		Description: "Build the read-only restore plan for a prepared handoff packet (the latest prepared packet when packet is omitted). This tool never records ack or verify events, never applies code, and never executes commands.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]propertySchema{
				"packet": {Type: "string", Description: "path to a prepared handoff packet JSON file (optional; defaults to the latest prepared packet)"},
				"repo":   {Type: "string", Description: "git working tree to compare against the packet (optional; defaults to the workspace directory)"},
			},
			AdditionalProperties: false,
		},
	},
}

// toolList returns the tool catalog. It is a function so callers cannot mutate
// the package-level definition.
func toolList() []toolDef {
	out := make([]toolDef, len(anchorToolList))
	copy(out, anchorToolList)
	return out
}

type toolsListResult struct {
	Tools []toolDef `json:"tools"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func toolErrorResult(message string) toolResult {
	return toolResult{Content: []toolContent{{Type: "text", Text: message}}, IsError: true}
}

// toolHandler executes one tool. A returned error is reported as a tool failure
// (result with isError=true), not as a JSON-RPC protocol error.
type toolHandler func(ctx context.Context, workspaceDir string, args json.RawMessage) (any, error)

var toolHandlers = map[string]toolHandler{
	"anchor_status":          statusTool,
	"anchor_tree":            treeTool,
	"anchor_evidence_verify": evidenceVerifyTool,
	"anchor_restore_plan":    restorePlanTool,
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool implements tools/call. Unknown tool names and missing names are
// invalid params (-32602). Tool-level failures become isError results.
func (s *server) callTool(raw json.RawMessage) (any, *rpcError) {
	trimmed := trimJSON(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: tools/call requires a tool name"}
	}
	var params callParams
	if err := json.Unmarshal(trimmed, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()}
	}
	if params.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: tools/call requires a tool name"}
	}
	handler, ok := toolHandlers[params.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: unknown tool " + params.Name}
	}
	args := trimJSON(params.Arguments)
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	result, err := handler(s.ctx, s.dir, args)
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	text, err := json.Marshal(result)
	if err != nil {
		return toolErrorResult("tool result cannot be encoded: " + err.Error()), nil
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: string(text)}}}, nil
}

func trimJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	start, end := 0, len(raw)
	for start < end && isJSONSpace(raw[start]) {
		start++
	}
	for end > start && isJSONSpace(raw[end-1]) {
		end--
	}
	return raw[start:end]
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// decodeArgs unmarshals tool arguments (absent/null arguments mean "{}").
func decodeArgs(raw json.RawMessage, dst any) error {
	trimmed := trimJSON(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte("{}")
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// anchor_status
// ---------------------------------------------------------------------------

type statusArgs struct {
	Repo string `json:"repo"`
}

type statusHandoff struct {
	Pending       bool   `json:"pending"`
	HandoffID     string `json:"handoff_id"`
	Acked         bool   `json:"acked"`
	Verified      bool   `json:"verified"`
	PacketMissing bool   `json:"packet_missing"`
	Forced        bool   `json:"forced"`
}

type statusTreeCounts struct {
	Nodes    int `json:"nodes"`
	Evidence int `json:"evidence"`
	Sessions int `json:"sessions"`
}

type statusOutput struct {
	WorkspaceID      string              `json:"workspace_id"`
	DeviceID         string              `json:"device_id"`
	ProjectionStatus string              `json:"projection_status"`
	RecoveryStatus   string              `json:"recovery_status"`
	RecoveryReasons  []string            `json:"recovery_reasons"`
	Handoff          statusHandoff       `json:"handoff"`
	Applied          int                 `json:"applied"`
	Pending          int                 `json:"pending"`
	Conflicts        int                 `json:"conflicts"`
	Tree             statusTreeCounts    `json:"tree"`
	Quarantined      []core.Quarantine   `json:"quarantined,omitempty"`
	Evaluation       *handoff.Evaluation `json:"evaluation,omitempty"`
	RecoveryRepoUsed string              `json:"recovery_repo,omitempty"`
}

func statusTool(_ context.Context, workspaceDir string, raw json.RawMessage) (any, error) {
	var args statusArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	store, err := core.Open(workspaceDir)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(store.Dir)
	if err != nil {
		return nil, err
	}
	report, err := store.LoadReport()
	if err != nil {
		return nil, err
	}
	replay, err := store.LoadReplay()
	if err != nil {
		return nil, err
	}
	gate, err := handoff.Gate(store.Dir)
	if err != nil {
		return nil, err
	}

	out := statusOutput{
		WorkspaceID:      manifest.WorkspaceID,
		DeviceID:         manifest.DeviceID,
		ProjectionStatus: replay.RecoveryStatus,
		RecoveryStatus:   replay.RecoveryStatus,
		RecoveryReasons:  []string{},
		Applied:          len(replay.Applied),
		Pending:          len(replay.Pending),
		Conflicts:        len(replay.Conflicts),
		Quarantined:      report.Quarantined,
		RecoveryRepoUsed: args.Repo,
		Handoff: statusHandoff{
			Pending:       gate.Pending,
			HandoffID:     gate.HandoffID,
			Acked:         gate.Progress.Acked,
			Verified:      gate.Progress.Verified,
			PacketMissing: gate.PacketMissing,
			Forced:        gate.Progress.Forced,
		},
	}
	if replay.Tree != nil {
		out.Tree = statusTreeCounts{
			Nodes:    len(replay.Tree.Nodes),
			Evidence: len(replay.Tree.Evidence),
			Sessions: len(replay.Tree.Sessions),
		}
	}
	if len(report.Quarantined) > 0 {
		out.ProjectionStatus = "incomplete"
		out.RecoveryStatus = "incomplete"
		out.RecoveryReasons = append(out.RecoveryReasons, "quarantined event files prevent a complete recovery")
	}
	if args.Repo == "" {
		// Without a repository the code state cannot be compared, so the
		// recovery status is the projection status and the caller is told why.
		out.RecoveryReasons = append(out.RecoveryReasons, "no repo argument provided; recovery_status reflects the projection only")
		if out.ProjectionStatus != "complete" && len(report.Quarantined) == 0 {
			out.RecoveryReasons = append(out.RecoveryReasons, "projection is incomplete (pending events or conflicts)")
		}
		return out, nil
	}

	var packet *handoff.Packet
	if p, _, ok := handoff.LatestPrepared(store.Dir); ok {
		packet = &p
	}
	eval := handoff.Evaluate(store.Dir, args.Repo, packet)
	out.Evaluation = &eval
	out.RecoveryStatus = eval.Status
	out.RecoveryReasons = append(out.RecoveryReasons, eval.Reasons...)
	if gate.Pending {
		out.RecoveryReasons = append(out.RecoveryReasons, "handoff "+gate.HandoffID+" is pending successor verification")
	}
	return out, nil
}

type manifestInfo struct {
	WorkspaceID string `json:"workspace_id"`
	DeviceID    string `json:"device_id"`
}

// readManifest reads the workspace manifest that core.Open already validated.
func readManifest(stateDir string) (manifestInfo, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "manifest.json"))
	if err != nil {
		return manifestInfo{}, fmt.Errorf("cannot read workspace manifest: %w", err)
	}
	var m manifestInfo
	if err := json.Unmarshal(b, &m); err != nil {
		return manifestInfo{}, fmt.Errorf("invalid workspace manifest: %w", err)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// anchor_tree
// ---------------------------------------------------------------------------

type treeArgs struct {
	IncludeDeleted bool `json:"include_deleted"`
	Limit          int  `json:"limit"`
}

type treeNode struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	ParentID   string `json:"parent_id"`
	Owner      string `json:"owner"`
	Provenance string `json:"provenance"`
	Deleted    bool   `json:"deleted"`
}

type treeEvidence struct {
	EvidenceID string `json:"evidence_id"`
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

type treeSessions struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

type treeOutput struct {
	Nodes    []treeNode     `json:"nodes"`
	Evidence []treeEvidence `json:"evidence"`
	Sessions treeSessions   `json:"sessions"`
}

func treeTool(_ context.Context, workspaceDir string, raw json.RawMessage) (any, error) {
	var args treeArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultTreeLimit
	}
	if limit > maxTreeLimit {
		limit = maxTreeLimit
	}
	store, err := core.Open(workspaceDir)
	if err != nil {
		return nil, err
	}
	replay, err := store.LoadReplay()
	if err != nil {
		return nil, err
	}

	out := treeOutput{Nodes: []treeNode{}, Evidence: []treeEvidence{}, Sessions: treeSessions{IDs: []string{}}}
	if replay.Tree == nil {
		return out, nil
	}
	for _, n := range replay.Tree.SortedNodes() {
		if n.Deleted && !args.IncludeDeleted {
			continue
		}
		if len(out.Nodes) >= limit {
			break
		}
		out.Nodes = append(out.Nodes, treeNode{
			ID:         n.ID,
			Type:       string(n.Type),
			Title:      n.Title,
			Status:     string(n.Status),
			ParentID:   n.ParentID,
			Owner:      n.Owner,
			Provenance: string(n.Provenance),
			Deleted:    n.Deleted,
		})
	}
	evidenceIDs := make([]string, 0, len(replay.Tree.Evidence))
	for id := range replay.Tree.Evidence {
		evidenceIDs = append(evidenceIDs, id)
	}
	sort.Strings(evidenceIDs)
	for _, id := range evidenceIDs {
		e := replay.Tree.Evidence[id]
		out.Evidence = append(out.Evidence, treeEvidence{
			EvidenceID: e.EvidenceID,
			NodeID:     e.NodeID,
			Kind:       e.Kind,
			SHA256:     e.SHA256,
			Size:       e.Size,
		})
	}
	sessionIDs := make([]string, 0, len(replay.Tree.Sessions))
	for id := range replay.Tree.Sessions {
		sessionIDs = append(sessionIDs, id)
	}
	sort.Strings(sessionIDs)
	out.Sessions = treeSessions{Count: len(sessionIDs), IDs: sessionIDs}
	return out, nil
}

// ---------------------------------------------------------------------------
// anchor_evidence_verify
// ---------------------------------------------------------------------------

type evidenceArgs struct {
	EvidenceID string `json:"evidence_id"`
}

type evidenceVerifyOutput struct {
	EvidenceID string `json:"evidence_id"`
	Verified   bool   `json:"verified"`
	Detail     string `json:"detail"`
}

func evidenceVerifyTool(_ context.Context, workspaceDir string, raw json.RawMessage) (any, error) {
	var args evidenceArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.EvidenceID == "" {
		return nil, fmt.Errorf("evidence_id is required")
	}
	store, err := core.Open(workspaceDir)
	if err != nil {
		return nil, err
	}
	replay, err := store.LoadReplay()
	if err != nil {
		return nil, err
	}
	if replay.Tree == nil {
		return nil, fmt.Errorf("unknown evidence id %q", args.EvidenceID)
	}
	entry, ok := replay.Tree.Evidence[args.EvidenceID]
	if !ok {
		return nil, fmt.Errorf("unknown evidence id %q", args.EvidenceID)
	}
	out := evidenceVerifyOutput{EvidenceID: args.EvidenceID, Verified: true,
		Detail: "artifact bytes match the recorded sha256 and size and contain no rejected secret material"}
	if err := core.VerifyEvidenceArtifact(store.Dir, entry); err != nil {
		out.Verified = false
		out.Detail = err.Error()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// anchor_restore_plan
// ---------------------------------------------------------------------------

type restorePlanArgs struct {
	Packet string `json:"packet"`
	Repo   string `json:"repo"`
}

func restorePlanTool(_ context.Context, workspaceDir string, raw json.RawMessage) (any, error) {
	var args restorePlanArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	store, err := core.Open(workspaceDir)
	if err != nil {
		return nil, err
	}
	var packet handoff.Packet
	var path string
	if args.Packet == "" {
		p, latestPath, ok := handoff.LatestPrepared(store.Dir)
		if !ok {
			return nil, fmt.Errorf("no prepared handoff")
		}
		packet, path = p, latestPath
	} else {
		packet, err = handoff.Load(args.Packet)
		if err != nil {
			return nil, fmt.Errorf("cannot load handoff packet %s: %w", args.Packet, err)
		}
		path = args.Packet
	}
	repo := args.Repo
	if repo == "" {
		repo = store.Dir
	}
	plan, err := handoff.BuildPlan(store.Dir, repo, packet, path)
	if err != nil {
		return nil, err
	}
	return plan, nil
}
