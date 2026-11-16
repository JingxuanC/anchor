package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

// wrapRecord is the local (non-event) record of one wrapped session.
type wrapRecord struct {
	SessionID      string   `json:"session_id"`
	NodeID         string   `json:"node_id"`
	Command        []string `json:"command"`
	ExitCode       int      `json:"exit_code"`
	StartedAt      string   `json:"started_at"`
	EndedAt        string   `json:"ended_at"`
	DurationMS     int64    `json:"duration_ms"`
	ContextFile    string   `json:"context_file"`
	HandoffID      string   `json:"handoff_id,omitempty"`
	HandoffPath    string   `json:"handoff_path,omitempty"`
	PrepareHandoff bool     `json:"prepare_handoff"`
	WorkspaceDir   string   `json:"workspace_dir"`
}

// cmdWrap runs an agent CLI without native hooks and records the session around
// it: the child inherits stdio and its exit code is propagated, while Anchor
// records session_seen/session_ended events and exports a context file plus
// ANCHOR_* environment variables the agent can read.
func cmdWrap(args []string) {
	fs := flag.NewFlagSet("wrap", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	node := fs.String("node", "", "node id to attach the wrapped session to")
	repo := fs.String("repo", "", "repository for --prepare-handoff (default: current directory)")
	prepare := fs.Bool("prepare-handoff", false, "prepare a handoff packet when the wrapped command exits")
	asJSON := fs.Bool("json", false, "JSON session summary on stderr")
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep == len(args)-1 {
		die(fmt.Errorf("usage: anchor wrap [flags] -- <agent command> [args...]"))
	}
	if err := fs.Parse(args[:sep]); err != nil {
		die(err)
	}
	child := args[sep+1:]
	if *node == "" {
		die(fmt.Errorf("--node is required (wrap attaches the session to an existing node)"))
	}
	store := openStore(*dir)
	replay, err := store.LoadReplay()
	if err != nil {
		die(err)
	}
	n, ok := replay.Tree.Nodes[*node]
	if !ok {
		die(fmt.Errorf("node %s not found in this workspace", *node))
	}
	if n.Deleted {
		die(fmt.Errorf("node %s is deleted; wrap needs a live node", *node))
	}

	started := time.Now().UTC()
	cwd, err := os.Getwd()
	if err != nil {
		die(err)
	}
	identity := "anchor-wrap|" + *node + "|" + strings.Join(child, "\x00") + "|" + cwd + "|" + started.Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(identity))
	sessionRef := "sha256:" + hex.EncodeToString(sum[:])
	cwdSum := sha256.Sum256([]byte(cwd))
	sessionID := core.AdapterSessionID("wrap", sessionRef)
	base := core.AdapterSessionPayload{
		SessionID: sessionID, SessionRef: sessionRef, NodeID: *node, Adapter: "wrap",
		HookEvent: "wrap_start", Timestamp: started.Format(time.RFC3339Nano),
		CwdRef: "sha256:" + hex.EncodeToString(cwdSum[:]), Source: "hook_unverified",
		TrustLevel: "unverified_adapter", SourceEventRef: sessionRef,
	}
	if _, _, err := store.Append(core.Event{Type: core.EvAdapterSessionSeen, CausalParents: []string{}, Payload: mustJSON(base)}); err != nil {
		die(fmt.Errorf("cannot record the wrapped session: %w", err))
	}

	contextFile, err := writeWrapContext(store.Dir, replay.Tree, *node, sessionID, cwd)
	if err != nil {
		die(err)
	}
	record := wrapRecord{
		SessionID: sessionID, NodeID: *node, Command: child, StartedAt: started.Format(time.RFC3339Nano),
		ContextFile: contextFile, PrepareHandoff: *prepare, WorkspaceDir: store.Dir,
	}

	recPath := filepath.Join(store.Dir, "local", "wrap", sanitizeSessionID(sessionID)+".json")
	childEnv := append(os.Environ(),
		"ANCHOR_WRAP=1",
		"ANCHOR_WORKSPACE="+store.Dir,
		"ANCHOR_NODE="+*node,
		"ANCHOR_SESSION="+sessionID,
		"ANCHOR_CONTEXT_FILE="+contextFile,
	)

	cmd := exec.Command(child[0], child[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = childEnv
	if err := cmd.Start(); err != nil {
		record.ExitCode = -1
		record.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.DurationMS = time.Since(started).Milliseconds()
		_ = writeWrapRecord(recPath, record)
		writeSessionEnd(store, base, -1)
		die(fmt.Errorf("cannot start %s: %w", child[0], err))
	}

	// Forward termination signals so the wrapped agent can shut down cleanly.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, wrapSignals()...)
	defer signal.Stop(sigs)
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	forwarding := true
	for forwarding {
		select {
		case sig := <-sigs:
			_ = cmd.Process.Signal(sig)
		case waitErr := <-waitCh:
			forwarding = false
			record.ExitCode = exitCodeOf(waitErr, cmd.ProcessState)
		}
	}

	record.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	record.DurationMS = time.Since(started).Milliseconds()
	if *prepare {
		repoDir := *repo
		if repoDir == "" {
			repoDir = cwd
		}
		packet, path, err := handoff.Prepare(store.Dir, repoDir)
		if err != nil {
			// The session is over; a handoff failure must not lose that fact.
			fmt.Fprintf(os.Stderr, "wrap: session recorded but handoff prepare failed: %v\n", err)
		} else {
			record.HandoffID, record.HandoffPath = packet.HandoffID, path
		}
	}
	writeSessionEnd(store, base, record.ExitCode)
	if err := writeWrapRecord(recPath, record); err != nil {
		fmt.Fprintf(os.Stderr, "wrap: cannot write the local wrap record: %v\n", err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(record, "", "  ")
		fmt.Fprintln(os.Stderr, string(b))
	} else {
		fmt.Fprintf(os.Stderr, "wrap: session %s node %s exit=%d (%dms)%s\n", sessionID, *node, record.ExitCode, record.DurationMS, handoffSuffix(record))
	}
	os.Exit(record.ExitCode)
}

// writeWrapContext writes the context file the wrapped agent can read, derived
// from the Session Tree (goal, constraints, decisions, next steps).
func writeWrapContext(stateDir string, tree *core.Tree, nodeID, sessionID, cwd string) (string, error) {
	content := buildContextMarkdown(tree, stateDir, nodeID, sessionID, cwd)
	wrapDir := filepath.Join(stateDir, "local", "wrap")
	if err := os.MkdirAll(wrapDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(wrapDir, sanitizeSessionID(sessionID)+".context.md")
	tmp, err := os.CreateTemp(wrapDir, ".context-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func primaryGoal(tree *core.Tree) *core.Node {
	var goals []*core.Node
	for _, n := range tree.Nodes {
		if string(n.Type) == "goal" {
			goals = append(goals, n)
		}
	}
	if len(goals) == 0 {
		return nil
	}
	sortNodes(goals)
	return goals[0]
}

func sortNodes(nodes []*core.Node) {
	// Deterministic order by event id (event ids embed an acquisition timestamp)
	// then by node id.
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			if nodes[j].LastEventID < nodes[i].LastEventID || (nodes[j].LastEventID == nodes[i].LastEventID && nodes[j].ID < nodes[i].ID) {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
	}
}

func writeSessionEnd(store *core.Store, base core.AdapterSessionPayload, exitCode int) {
	ended := base
	ended.HookEvent = "wrap_end"
	ended.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	// The exit code is metadata, not a secret; ScanSecrets only inspects strings.
	ended.Tool = fmt.Sprintf("exit:%d", exitCode)
	if _, _, err := store.Append(core.Event{Type: core.EvAdapterSessionEnded, CausalParents: []string{}, Payload: mustJSON(ended)}); err != nil {
		fmt.Fprintf(os.Stderr, "wrap: cannot record session end: %v\n", err)
	}
}

func writeWrapRecord(path string, record wrapRecord) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wrap-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func handoffSuffix(r wrapRecord) string {
	if r.HandoffID == "" {
		return ""
	}
	return " handoff=" + r.HandoffID
}

func sanitizeSessionID(id string) string {
	return strings.NewReplacer(":", "_", "/", "_", " ", "_").Replace(id)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
