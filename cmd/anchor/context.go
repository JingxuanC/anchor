package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

// buildContextMarkdown renders the durable context an agent should read at
// session start (goal constraints, decisions, tasks, open questions, handoff).
func buildContextMarkdown(tree *core.Tree, stateDir, nodeID, sessionID, cwd string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Anchor session context\n\n")
	fmt.Fprintf(&b, "- workspace: %s\n", stateDir)
	if nodeID != "" {
		fmt.Fprintf(&b, "- node: %s\n", nodeID)
	}
	if sessionID != "" {
		fmt.Fprintf(&b, "- session: %s\n", sessionID)
	}
	fmt.Fprintf(&b, "- cwd: %s\n- generated_at: %s\n\n", cwd, time.Now().UTC().Format(time.RFC3339))
	if p, path, ok := handoff.LatestPrepared(stateDir); ok {
		fmt.Fprintf(&b, "Prepared handoff: %s\n  packet: %s\n  code_head: %s\n\n", p.HandoffID, path, p.CodeState.Head)
	} else {
		fmt.Fprintf(&b, "No prepared handoff yet.\n\n")
	}
	if n, ok := tree.Nodes[nodeID]; ok {
		fmt.Fprintf(&b, "## Current node\n\n- %s `%s` (%s): %s\n\n", n.Type, n.ID, n.Status, n.Title)
	}
	if g := primaryGoal(tree); g != nil {
		fmt.Fprintf(&b, "## Goal\n\n- %s: %s\n", g.ID, g.Title)
		if g.NorthStar != "" {
			fmt.Fprintf(&b, "- north star: %s\n", g.NorthStar)
		}
		for _, m := range g.Must {
			fmt.Fprintf(&b, "- must: %s\n", m)
		}
		for _, m := range g.MustNot {
			fmt.Fprintf(&b, "- must not: %s\n", m)
		}
		for _, s := range g.SuccessCriteria {
			fmt.Fprintf(&b, "- success: %s\n", s)
		}
		b.WriteString("\n")
	}
	var decisions, tasks, questions []*core.Node
	for _, n := range tree.Nodes {
		switch string(n.Type) {
		case "decision":
			decisions = append(decisions, n)
		case "task":
			tasks = append(tasks, n)
		case "question":
			questions = append(questions, n)
		}
	}
	sortNodes(decisions)
	sortNodes(tasks)
	sortNodes(questions)
	if len(decisions) > 0 {
		b.WriteString("## Decisions\n\n")
		for _, d := range decisions {
			fmt.Fprintf(&b, "- `%s` %s", d.ID, d.Title)
			if d.Decision != "" {
				fmt.Fprintf(&b, ": %s", d.Decision)
			}
			if d.Rationale != "" {
				fmt.Fprintf(&b, " (rationale: %s)", d.Rationale)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(tasks) > 0 {
		b.WriteString("## Tasks\n\n")
		for _, t := range tasks {
			fmt.Fprintf(&b, "- `%s` [%s] %s\n", t.ID, t.Status, t.Title)
		}
		b.WriteString("\n")
	}
	if len(questions) > 0 {
		b.WriteString("## Open questions\n\n")
		for _, q := range questions {
			fmt.Fprintf(&b, "- `%s` %s\n", q.ID, q.Title)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Anchor commands\n\n")
	fmt.Fprintf(&b, "- `anchor status --dir %q --json` — workspace state and handoff gate\n", stateDir)
	fmt.Fprintf(&b, "- `anchor tree --dir %q` — the goal/decision/task tree\n", stateDir)
	fmt.Fprintf(&b, "- `anchor node create|transition|edit ...` — record goals, decisions, tasks\n")
	fmt.Fprintf(&b, "- `anchor evidence add ...` — attach verifiable evidence\n")
	fmt.Fprintf(&b, "- `anchor handoff prepare --dir %q --repo <repo>` — prepare the successor handoff\n", stateDir)
	fmt.Fprintf(&b, "- `anchor handoff restore --dir %q --repo <repo> --packet <packet>` — successor ack/verify\n", stateDir)
	b.WriteString("\nRead this file instead of relying on chat history; Anchor is the durable memory.\n")
	return b.String()
}
