package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func treeFixture(t *testing.T) (root, proj string) {
	t.Helper()
	root = t.TempDir()
	proj = filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "init", "--attach", "--dir", proj, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	for _, args := range [][]string{
		{"node", "create", "--dir", proj, "--id", "goal", "--type", "goal", "--title", "Ship rate limiter", "--north-star", "no dropped requests", "--must", "preserve injected clock", "--must-not", "block on sleep"},
		{"node", "create", "--dir", proj, "--id", "dec1", "--type", "decision", "--parent", "goal", "--title", "Whole-interval rounding", "--decision", "round to whole intervals", "--rationale", "hidden tests expect boundaries"},
		{"node", "create", "--dir", proj, "--id", "task1", "--type", "task", "--parent", "goal", "--title", "Implement WaitTime"},
		{"node", "create", "--dir", proj, "--id", "q1", "--type", "question", "--parent", "goal", "--title", "What about bursts?"},
		{"node", "create", "--dir", proj, "--id", "note1", "--type", "note", "--parent", "goal", "--title", "scratch"},
	} {
		if _, stderr, code := runCLI(t, root, args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}
	return root, proj
}

func TestCLITreeTextAndJSON(t *testing.T) {
	root, proj := treeFixture(t)
	out, stderr, code := runCLI(t, root, "tree", "--dir", proj)
	if code != 0 {
		t.Fatalf("tree: %s", stderr)
	}
	for _, want := range []string{"workspace ws1", "GOAL goal", "north star: no dropped requests", "must: preserve injected clock",
		"must not: block on sleep", "DECISION dec1", "decision: round to whole intervals", "rationale: hidden tests expect boundaries",
		"TASK task1", "QUESTION q1", "NOTE note1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text tree missing %q:\n%s", want, out)
		}
	}
	jsonOut, stderr, code := runCLI(t, root, "tree", "--dir", proj, "--json")
	if code != 0 {
		t.Fatalf("tree --json: %s", stderr)
	}
	var view struct {
		SchemaVersion    int    `json:"schema_version"`
		WorkspaceID      string `json:"workspace_id"`
		DeviceID         string `json:"device_id"`
		ProjectionStatus string `json:"projection_status"`
		Nodes            []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			ParentID string `json:"parent_id"`
			Status   string `json:"status"`
			Decision string `json:"decision"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &view); err != nil {
		t.Fatal(err)
	}
	if view.SchemaVersion != 1 || view.WorkspaceID != "ws1" || view.DeviceID != "dev1" || view.ProjectionStatus != "complete" {
		t.Fatalf("tree JSON meta wrong: %s", jsonOut)
	}
	if len(view.Nodes) != 5 {
		t.Fatalf("nodes = %d, want 5", len(view.Nodes))
	}
	// Goals first, then decisions/tasks; deterministic order.
	if view.Nodes[0].ID != "goal" || view.Nodes[1].ID != "dec1" {
		t.Fatalf("tree JSON order wrong: %+v", view.Nodes)
	}
	if view.Nodes[1].Decision != "round to whole intervals" {
		t.Fatalf("decision detail lost: %+v", view.Nodes[1])
	}
	// Deterministic across runs (generated_at aside).
	again, _, _ := runCLI(t, root, "tree", "--dir", proj, "--json")
	if stripGeneratedAt(t, jsonOut) != stripGeneratedAt(t, again) {
		t.Fatal("tree JSON is not deterministic")
	}
}

func stripGeneratedAt(t *testing.T, s string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "generated_at")
	b, _ := json.Marshal(m)
	return string(b)
}

func TestCLITreeMarkdownAndHTML(t *testing.T) {
	root, proj := treeFixture(t)
	mdPath := filepath.Join(root, "TREE.md")
	if _, stderr, code := runCLI(t, root, "tree", "--dir", proj, "--md", mdPath); code != 0 {
		t.Fatalf("tree --md: %s", stderr)
	}
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Anchor Session Tree", "## Goal `goal`", "- north star: no dropped requests", "## Decision `dec1`", "- rationale: hidden tests expect boundaries"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	htmlPath := filepath.Join(root, "TREE.html")
	if _, stderr, code := runCLI(t, root, "tree", "--dir", proj, "--html", htmlPath); code != 0 {
		t.Fatalf("tree --html: %s", stderr)
	}
	page, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	if !strings.HasPrefix(text, "<!doctype html>") || !strings.Contains(text, "</html>") {
		t.Fatalf("html not self-contained: %s", text[:80])
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatal("html must not reference external resources")
	}
	for _, want := range []string{"Anchor Session Tree", "Ship rate limiter", "round to whole intervals", "hidden tests expect boundaries"} {
		if !strings.Contains(text, want) {
			t.Fatalf("html missing %q", want)
		}
	}
}

func TestCLITreeEscapesHTMLAndHidesDeleted(t *testing.T) {
	root, proj := treeFixture(t)
	// A title with markup must be escaped, not injected.
	if _, stderr, code := runCLI(t, root, "node", "create", "--dir", proj, "--id", "xss", "--type", "note", "--parent", "goal", "--title", `<script>alert(1)</script>`); code != 0 {
		t.Fatalf("xss node: %s", stderr)
	}
	htmlPath := filepath.Join(root, "TREE.html")
	if _, stderr, code := runCLI(t, root, "tree", "--dir", proj, "--html", htmlPath); code != 0 {
		t.Fatalf("tree --html: %s", stderr)
	}
	page, _ := os.ReadFile(htmlPath)
	if strings.Contains(string(page), "<script>alert(1)</script>") {
		t.Fatal("tree HTML did not escape node titles")
	}
	if !strings.Contains(string(page), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("escaped title missing from tree HTML")
	}
	// Tombstoned nodes are hidden unless requested.
	if _, stderr, code := runCLI(t, root, "node", "tombstone", "--dir", proj, "--id", "note1"); code != 0 {
		t.Fatalf("delete: %s", stderr)
	}
	out, _, _ := runCLI(t, root, "tree", "--dir", proj)
	if strings.Contains(out, "NOTE note1") {
		t.Fatal("deleted node shown by default")
	}
	out, _, _ = runCLI(t, root, "tree", "--dir", proj, "--include-deleted")
	if !strings.Contains(out, "NOTE note1") || !strings.Contains(out, "[deleted]") {
		t.Fatalf("deleted node missing with --include-deleted:\n%s", out)
	}
}

func TestCLITreeEmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runCLI(t, root, "init", "--attach", "--dir", proj, "--workspace", "ws1", "--device", "dev1"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	out, stderr, code := runCLI(t, root, "tree", "--dir", proj)
	if code != 0 {
		t.Fatalf("empty tree: %s", stderr)
	}
	if !strings.Contains(out, "no nodes yet") {
		t.Fatalf("empty tree hint missing: %s", out)
	}
}

func TestCLIStateAliasMatchesStatus(t *testing.T) {
	root, proj := treeFixture(t)
	statusOut, _, code := runCLI(t, root, "status", "--dir", proj, "--json")
	if code != 0 {
		t.Fatal("status failed")
	}
	stateOut, _, code := runCLI(t, root, "state", "--dir", proj, "--json")
	if code != 0 {
		t.Fatal("state failed")
	}
	if stripGeneratedAt(t, statusOut) != stripGeneratedAt(t, stateOut) {
		t.Fatal("anchor state is not an alias of anchor status")
	}
}
