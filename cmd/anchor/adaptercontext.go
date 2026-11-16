package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/JingxuanC/anchor/internal/core"
)

const defaultContextMaxBytes = 8000

// adapterContext implements the SessionStart hook: it renders the durable Anchor
// context and returns it as Claude Code hook JSON
// ({"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":…}}).
// It is also usable manually with --print.
func adapterContext(args []string) {
	fs := flag.NewFlagSet("adapter context", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	stdin := fs.Bool("stdin", false, "read the hook event JSON from stdin")
	eventFile := fs.String("event", "", "read the hook event JSON from a file")
	maxBytes := fs.Int("max-bytes", defaultContextMaxBytes, "maximum context size in bytes")
	printOnly := fs.Bool("print", false, "print the human-readable context instead of hook JSON")
	_ = fs.Parse(args)
	if (*eventFile == "") == !*stdin {
		die(fmt.Errorf("exactly one of --event or --stdin is required"))
	}
	store := openStore(*dir)
	hook, err := readHookInput(*eventFile, *stdin)
	if err != nil {
		die(err)
	}
	replay, err := store.LoadReplay()
	if err != nil {
		die(err)
	}
	cwd := hook.Cwd
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	nodeID := activeNodeID(replay.Tree)
	content := buildContextMarkdown(replay.Tree, store.Dir, nodeID, hook.SessionID, cwd)
	content = truncateContext(content, *maxBytes)
	// Fail closed on secrets but never break the agent's session: refuse to
	// inject and explain on stderr.
	if err := core.ScanSecrets([]byte(content)); err != nil {
		fmt.Fprintf(os.Stderr, "anchor: context not injected: %v\n", err)
		if !*printOnly {
			_, _ = io.WriteString(os.Stdout, "{\"continue\":true}\n")
		}
		return
	}
	if *printOnly {
		fmt.Print(content)
		return
	}
	out := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     "SessionStart",
		"additionalContext": content,
	}}
	b, err := json.Marshal(out)
	if err != nil {
		die(err)
	}
	os.Stdout.Write(append(b, '\n'))
}

type hookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Source    string `json:"source"`
}

func readHookInput(path string, useStdin bool) (hookInput, error) {
	var in io.Reader
	if useStdin {
		in = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return hookInput{}, err
		}
		defer f.Close()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, 1<<20))
	if err != nil {
		return hookInput{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return hookInput{}, nil
	}
	var h hookInput
	if err := json.Unmarshal(data, &h); err != nil {
		return hookInput{}, fmt.Errorf("invalid hook event JSON: %w", err)
	}
	return h, nil
}

// activeNodeID picks the node a fresh session is most likely attached to: an
// in-progress task, else an in-progress goal, else the primary goal.
func activeNodeID(tree *core.Tree) string {
	nodes := tree.SortedNodes()
	for _, n := range nodes {
		if n.Type == core.NodeTask && n.Status == core.StatusInProgress && !n.Deleted {
			return n.ID
		}
	}
	for _, n := range nodes {
		if n.Type == core.NodeGoal && n.Status == core.StatusInProgress && !n.Deleted {
			return n.ID
		}
	}
	if g := primaryGoal(tree); g != nil {
		return g.ID
	}
	return ""
}

func truncateContext(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		cut = cut[:idx]
	}
	return cut + "\n\n… (context truncated; run `anchor tree` / `anchor status --json` for the full state)\n"
}
