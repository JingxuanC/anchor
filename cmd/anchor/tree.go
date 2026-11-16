package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

// treeView is the stable JSON projection printed by `anchor tree --json`.
type treeView struct {
	SchemaVersion    int            `json:"schema_version"`
	GeneratedAt      string         `json:"generated_at"`
	WorkspaceID      string         `json:"workspace_id"`
	DeviceID         string         `json:"device_id"`
	ProjectionStatus string         `json:"projection_status"`
	Nodes            []treeNodeView `json:"nodes"`
	Evidence         []treeEvidence `json:"evidence,omitempty"`
	Sessions         int            `json:"sessions"`
	Handoff          map[string]any `json:"handoff,omitempty"`
}

type treeNodeView struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	ParentID     string   `json:"parent_id,omitempty"`
	Owner        string   `json:"owner,omitempty"`
	LeaseID      string   `json:"lease_id,omitempty"`
	FencingToken uint64   `json:"fencing_token,omitempty"`
	Provenance   string   `json:"provenance,omitempty"`
	Deleted      bool     `json:"deleted,omitempty"`
	NorthStar    string   `json:"north_star,omitempty"`
	Must         []string `json:"must,omitempty"`
	MustNot      []string `json:"must_not,omitempty"`
	Success      []string `json:"success_criteria,omitempty"`
	Decision     string   `json:"decision,omitempty"`
	Rationale    string   `json:"rationale,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	Consequences []string `json:"consequences,omitempty"`
	Note         string   `json:"note,omitempty"`
	Evidence     int      `json:"evidence"`
}

type treeEvidence struct {
	EvidenceID string `json:"evidence_id"`
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

var nodeTypeRank = map[core.NodeType]int{
	core.NodeGoal: 0, core.NodeDecision: 1, core.NodeBranch: 2, core.NodeTask: 3, core.NodeQuestion: 4, core.NodeNote: 5,
}

func cmdTree(args []string) {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	asJSON := fs.Bool("json", false, "JSON tree")
	mdPath := fs.String("md", "", "write a Markdown tree to FILE ('-' for stdout)")
	htmlPath := fs.String("html", "", "write a self-contained HTML tree to FILE ('-' for stdout)")
	includeDeleted := fs.Bool("include-deleted", false, "include tombstoned nodes")
	_ = fs.Parse(args)

	store := openStore(*dir)
	replay, err := store.LoadReplay()
	if err != nil {
		die(err)
	}
	m, err := readManifest(*dir)
	if err != nil {
		die(err)
	}
	view := buildTreeView(store.Dir, m, replay, *includeDeleted)
	switch {
	case *asJSON:
		b, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			die(err)
		}
		fmt.Println(string(b))
	case *mdPath != "":
		writeTreeOutput(*mdPath, renderTreeMarkdown(view))
	case *htmlPath != "":
		writeTreeOutput(*htmlPath, renderTreeHTML(view))
	default:
		fmt.Print(renderTreeText(view))
	}
}

func writeTreeOutput(path, content string) {
	if path == "-" {
		fmt.Print(content)
		return
	}
	if err := writeFileAtomic(path, content); err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "tree written to %s\n", path)
}

func writeFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tree-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func buildTreeView(workspaceDir string, m manifest, replay core.ReplayResult, includeDeleted bool) treeView {
	tree := replay.Tree
	evidenceCount := map[string]int{}
	view := treeView{
		SchemaVersion:    1,
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		WorkspaceID:      m.WorkspaceID,
		DeviceID:         m.DeviceID,
		ProjectionStatus: replay.RecoveryStatus,
		Sessions:         len(tree.Sessions),
	}
	for _, e := range tree.Evidence {
		evidenceCount[e.NodeID]++
		view.Evidence = append(view.Evidence, treeEvidence{EvidenceID: e.EvidenceID, NodeID: e.NodeID, Kind: e.Kind, SHA256: e.SHA256, Size: e.Size})
	}
	sort.Slice(view.Evidence, func(i, j int) bool { return view.Evidence[i].EvidenceID < view.Evidence[j].EvidenceID })

	nodes := tree.SortedNodes()
	for _, n := range nodes {
		if n.Deleted && !includeDeleted {
			continue
		}
		view.Nodes = append(view.Nodes, treeNodeView{
			ID: n.ID, Type: string(n.Type), Title: n.Title, Status: string(n.Status), ParentID: n.ParentID,
			Owner: n.Owner, LeaseID: n.LeaseID, FencingToken: n.FencingToken, Provenance: string(n.Provenance),
			Deleted: n.Deleted, NorthStar: n.NorthStar, Must: n.Must, MustNot: n.MustNot, Success: n.SuccessCriteria,
			Decision: n.Decision, Rationale: n.Rationale, Alternatives: n.Alternatives, Consequences: n.Consequences,
			Note: n.Note, Evidence: evidenceCount[n.ID],
		})
	}
	sort.SliceStable(view.Nodes, func(i, j int) bool {
		ri, rj := nodeTypeRank[core.NodeType(view.Nodes[i].Type)], nodeTypeRank[core.NodeType(view.Nodes[j].Type)]
		if ri != rj {
			return ri < rj
		}
		if view.Nodes[i].ID != view.Nodes[j].ID {
			return view.Nodes[i].ID < view.Nodes[j].ID
		}
		return false
	})
	if gate, err := handoff.Gate(workspaceDir); err == nil && gate.HandoffID != "" {
		view.Handoff = map[string]any{"pending": gate.Pending, "handoff_id": gate.HandoffID,
			"acked": gate.Progress.Acked, "verified": gate.Progress.Verified,
			"packet_missing": gate.PacketMissing, "forced": gate.Progress.Forced}
	}
	return view
}

// treeLayout groups nodes for rendering: children by parent, roots keep the
// goal-first order.
type treeLayout struct {
	roots    []treeNodeView
	children map[string][]treeNodeView
}

func layoutTree(nodes []treeNodeView) treeLayout {
	byID := map[string]bool{}
	for _, n := range nodes {
		byID[n.ID] = true
	}
	l := treeLayout{children: map[string][]treeNodeView{}}
	for _, n := range nodes {
		if n.ParentID != "" && byID[n.ParentID] {
			l.children[n.ParentID] = append(l.children[n.ParentID], n)
		} else {
			l.roots = append(l.roots, n)
		}
	}
	return l
}

func statusBadge(n treeNodeView) string {
	badge := "[" + n.Status + "]"
	if n.Deleted {
		badge = "[deleted]"
	}
	if n.Owner != "" {
		badge += " @" + n.Owner
	}
	return badge
}

func renderTreeText(view treeView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workspace %s (device %s)  projection: %s  nodes: %d  evidence: %d  sessions: %d\n",
		orNone(view.WorkspaceID), orNone(view.DeviceID), view.ProjectionStatus, len(view.Nodes), len(view.Evidence), view.Sessions)
	if view.Handoff != nil {
		fmt.Fprintf(&b, "handoff %v pending=%v acked=%v verified=%v%s\n",
			view.Handoff["handoff_id"], view.Handoff["pending"], view.Handoff["acked"], view.Handoff["verified"],
			func() string {
				if missing, _ := view.Handoff["packet_missing"].(bool); missing {
					return " (packet missing)"
				}
				return ""
			}())
	}
	if len(view.Nodes) == 0 {
		b.WriteString("\n(no nodes yet; create one with `anchor node create`)\n")
		return b.String()
	}
	b.WriteString("\n")
	l := layoutTree(view.Nodes)
	for _, root := range l.roots {
		renderTextNode(&b, root, 0, l, true)
	}
	return b.String()
}

func renderTextNode(b *strings.Builder, n treeNodeView, depth int, l treeLayout, expanded bool) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(b, "%s%s %s %s — %s\n", indent, strings.ToUpper(n.Type), n.ID, statusBadge(n), n.Title)
	detailIndent := strings.Repeat("  ", depth+1)
	if n.Type == string(core.NodeGoal) {
		if n.NorthStar != "" {
			fmt.Fprintf(b, "%snorth star: %s\n", detailIndent, n.NorthStar)
		}
		for _, m := range n.Must {
			fmt.Fprintf(b, "%smust: %s\n", detailIndent, m)
		}
		for _, m := range n.MustNot {
			fmt.Fprintf(b, "%smust not: %s\n", detailIndent, m)
		}
		for _, s := range n.Success {
			fmt.Fprintf(b, "%ssuccess: %s\n", detailIndent, s)
		}
	}
	if n.Type == string(core.NodeDecision) {
		if n.Decision != "" {
			fmt.Fprintf(b, "%sdecision: %s\n", detailIndent, n.Decision)
		}
		if n.Rationale != "" {
			fmt.Fprintf(b, "%srationale: %s\n", detailIndent, n.Rationale)
		}
		for _, a := range n.Alternatives {
			fmt.Fprintf(b, "%salternative: %s\n", detailIndent, a)
		}
		for _, c := range n.Consequences {
			fmt.Fprintf(b, "%sconsequence: %s\n", detailIndent, c)
		}
	}
	if n.Note != "" {
		fmt.Fprintf(b, "%snote: %s\n", detailIndent, n.Note)
	}
	if n.Evidence > 0 {
		fmt.Fprintf(b, "%sevidence: %d blob(s)\n", detailIndent, n.Evidence)
	}
	for _, child := range l.children[n.ID] {
		renderTextNode(b, child, depth+1, l, expanded)
	}
}

func renderTreeMarkdown(view treeView) string {
	var b strings.Builder
	b.WriteString("# Anchor Session Tree\n\n")
	fmt.Fprintf(&b, "- workspace: %s\n- device: %s\n- projection: %s\n- nodes: %d\n- evidence: %d\n- sessions: %d\n- generated: %s\n\n",
		orNone(view.WorkspaceID), orNone(view.DeviceID), view.ProjectionStatus, len(view.Nodes), len(view.Evidence), view.Sessions, view.GeneratedAt)
	if view.Handoff != nil {
		fmt.Fprintf(&b, "- handoff: %v (pending=%v acked=%v verified=%v)\n\n",
			view.Handoff["handoff_id"], view.Handoff["pending"], view.Handoff["acked"], view.Handoff["verified"])
	}
	if len(view.Nodes) == 0 {
		b.WriteString("_No nodes yet._\n")
		return b.String()
	}
	l := layoutTree(view.Nodes)
	for _, root := range l.roots {
		renderMarkdownNode(&b, root, 1, l)
	}
	return b.String()
}

func renderMarkdownNode(b *strings.Builder, n treeNodeView, depth int, l treeLayout) {
	heading := strings.Repeat("#", minInt(depth+1, 6))
	status := n.Status
	if n.Deleted {
		status = "deleted"
	}
	fmt.Fprintf(b, "%s %s `%s` — %s\n\n", heading, humanType(n.Type), n.ID, n.Title)
	fmt.Fprintf(b, "- status: %s\n", status)
	if n.Owner != "" {
		fmt.Fprintf(b, "- owner: %s (fencing token %d)\n", n.Owner, n.FencingToken)
	}
	if n.NorthStar != "" {
		fmt.Fprintf(b, "- north star: %s\n", n.NorthStar)
	}
	for _, m := range n.Must {
		fmt.Fprintf(b, "- must: %s\n", m)
	}
	for _, m := range n.MustNot {
		fmt.Fprintf(b, "- must not: %s\n", m)
	}
	for _, s := range n.Success {
		fmt.Fprintf(b, "- success: %s\n", s)
	}
	if n.Decision != "" {
		fmt.Fprintf(b, "- decision: %s\n", n.Decision)
	}
	if n.Rationale != "" {
		fmt.Fprintf(b, "- rationale: %s\n", n.Rationale)
	}
	for _, a := range n.Alternatives {
		fmt.Fprintf(b, "- alternative: %s\n", a)
	}
	for _, c := range n.Consequences {
		fmt.Fprintf(b, "- consequence: %s\n", c)
	}
	if n.Note != "" {
		fmt.Fprintf(b, "- note: %s\n", n.Note)
	}
	if n.Evidence > 0 {
		fmt.Fprintf(b, "- evidence blobs: %d\n", n.Evidence)
	}
	b.WriteString("\n")
	for _, child := range l.children[n.ID] {
		renderMarkdownNode(b, child, depth+1, l)
	}
}

func renderTreeHTML(view treeView) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">\n")
	fmt.Fprintf(&b, "<title>Anchor tree — %s</title>\n", html.EscapeString(orNone(view.WorkspaceID)))
	b.WriteString(`<style>
:root{color-scheme:light dark}body{font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;max-width:980px;margin:2rem auto;padding:0 1rem;background:#fff;color:#111}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}a{color:#8ab4f8}}
h1{font-size:1.4rem;margin-bottom:.2rem}.meta{color:#777;font-size:.9rem;margin-bottom:1.2rem}
details{margin:.35rem 0 .35rem 1rem;border-left:2px solid #ddd;padding-left:.8rem}details>summary{cursor:pointer}
.node{display:inline-block}.type{font-weight:600;text-transform:uppercase;font-size:.75rem;letter-spacing:.04em;color:#666;margin-right:.35rem}
.id{font-family:ui-monospace,monospace;font-size:.85rem;color:#555;margin-right:.35rem}
.status{font-size:.75rem;border-radius:999px;padding:.05rem .45rem;background:#eef;color:#234;margin-left:.35rem}
.s-in_progress{background:#fff3cd;color:#664d03}.s-done{background:#d1e7dd;color:#0f5132}.s-blocked{background:#f8d7da;color:#842029}.s-abandoned{background:#e2e3e5;color:#41464b}.s-deleted{background:#eee;color:#999}
.kv{margin:.25rem 0 0 1rem;color:#333}.kv b{font-weight:600}ul.ev{margin:.2rem 0 0 1rem;color:#555;font-size:.85rem}
</style></head><body>`)
	fmt.Fprintf(&b, "<h1>Anchor Session Tree</h1>\n<div class=\"meta\">workspace %s · device %s · projection %s · nodes %d · evidence %d · sessions %d · generated %s</div>\n",
		html.EscapeString(orNone(view.WorkspaceID)), html.EscapeString(orNone(view.DeviceID)),
		html.EscapeString(view.ProjectionStatus), len(view.Nodes), len(view.Evidence), view.Sessions, html.EscapeString(view.GeneratedAt))
	if view.Handoff != nil {
		fmt.Fprintf(&b, "<div class=\"meta\">handoff %s · pending=%v · acked=%v · verified=%v</div>\n",
			html.EscapeString(fmt.Sprint(view.Handoff["handoff_id"])), view.Handoff["pending"], view.Handoff["acked"], view.Handoff["verified"])
	}
	if len(view.Nodes) == 0 {
		b.WriteString("<p>No nodes yet.</p>\n")
	} else {
		l := layoutTree(view.Nodes)
		for _, root := range l.roots {
			renderHTMLNode(&b, root, l)
		}
	}
	b.WriteString("</body></html>\n")
	return b.String()
}

func renderHTMLNode(b *strings.Builder, n treeNodeView, l treeLayout) {
	statusClass := "s-" + strings.ReplaceAll(n.Status, " ", "_")
	if n.Deleted {
		statusClass = "s-deleted"
	}
	fmt.Fprintf(b, "<details open><summary><span class=\"type\">%s</span><span class=\"id\">%s</span>%s<span class=\"status %s\">%s</span></summary>\n",
		html.EscapeString(humanType(n.Type)), html.EscapeString(n.ID), html.EscapeString(n.Title), html.EscapeString(statusClass), html.EscapeString(func() string {
			if n.Deleted {
				return "deleted"
			}
			return n.Status
		}()))
	value := func(label, v string) {
		if v != "" {
			fmt.Fprintf(b, "<div class=\"kv\"><b>%s:</b> %s</div>\n", html.EscapeString(label), html.EscapeString(v))
		}
	}
	value("north star", n.NorthStar)
	for _, m := range n.Must {
		value("must", m)
	}
	for _, m := range n.MustNot {
		value("must not", m)
	}
	for _, s := range n.Success {
		value("success", s)
	}
	value("decision", n.Decision)
	value("rationale", n.Rationale)
	for _, a := range n.Alternatives {
		value("alternative", a)
	}
	for _, c := range n.Consequences {
		value("consequence", c)
	}
	value("note", n.Note)
	if n.Evidence > 0 {
		fmt.Fprintf(b, "<div class=\"kv\"><b>evidence:</b> %d blob(s)</div>\n", n.Evidence)
	}
	if n.Owner != "" {
		fmt.Fprintf(b, "<div class=\"kv\"><b>owner:</b> %s · fencing token %d</div>\n", html.EscapeString(n.Owner), n.FencingToken)
	}
	for _, child := range l.children[n.ID] {
		renderHTMLNode(b, child, l)
	}
	b.WriteString("</details>\n")
}

func humanType(t string) string {
	switch t {
	case string(core.NodeGoal):
		return "Goal"
	case string(core.NodeDecision):
		return "Decision"
	case string(core.NodeTask):
		return "Task"
	case string(core.NodeBranch):
		return "Branch"
	case string(core.NodeQuestion):
		return "Question"
	case string(core.NodeNote):
		return "Note"
	}
	return t
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// orNone renders an empty value as "(unknown)" for human output.
func orNone(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
