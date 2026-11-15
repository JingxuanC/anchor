package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/JingxuanC/anchor/internal/adapter"
	"github.com/JingxuanC/anchor/internal/core"
)

const maxCaptureInput = 1 << 20

func cmdAdapter(args []string) {
	if len(args) < 1 {
		adapterUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "capabilities":
		adapterCapabilities(args[1:])
	case "render":
		adapterRender(args[1:])
	case "capture":
		adapterCapture(args[1:])
	case "ingest":
		adapterIngest(args[1:])
	default:
		adapterUsage()
		os.Exit(2)
	}
}

func adapterUsage() {
	fmt.Fprintln(os.Stderr, `  anchor adapter capabilities [--json]
  anchor adapter render --adapter ID (--target DIR | --print)
  anchor adapter capture --adapter ID (--event FILE | --stdin) (--out FILE | --print)
  anchor adapter ingest --adapter ID --dir WS --node NODE (--event FILE | --stdin) [--source fixture|hook_unverified] [--json]`)
}

func adapterCapabilities(args []string) {
	fs := flag.NewFlagSet("adapter capabilities", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	all := adapter.All()
	if *asJSON {
		b, _ := json.MarshalIndent(map[string]any{"adapters": all, "evidence_grades": map[string]string{"D": "documentation reviewed", "V": "verified in this environment", "S": "supported end-to-end"}, "verified_native_hooks": false}, "", "  ")
		fmt.Println(string(b))
		return
	}
	for _, a := range all {
		fmt.Printf("%-12s grade=%s native_hook=%s e2e=%v\n", a.ID, a.EvidenceGrade, a.NativeHookStatus, a.EndToEndVerified)
	}
}

func adapterRender(args []string) {
	fs := flag.NewFlagSet("adapter render", flag.ExitOnError)
	id := fs.String("adapter", "", "adapter id")
	target := fs.String("target", "", "explicit output directory")
	printOut := fs.Bool("print", false, "print dry-run output; write nothing")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--adapter is required"))
	}
	if *target == "" && !*printOut {
		die(fmt.Errorf("--target or --print is required; no default config path is used"))
	}
	if *printOut {
		out, files, err := adapter.Render(*id)
		if err != nil {
			die(err)
		}
		contents := []map[string]any{}
		for _, f := range files {
			contents = append(contents, map[string]any{"path": f.Path, "content": string(f.Content)})
		}
		b, _ := json.MarshalIndent(map[string]any{"rendered": out, "files": contents, "dry_run": true}, "", "  ")
		fmt.Println(string(b))
		return
	}
	out, paths, err := adapter.Write(*id, *target)
	if err != nil {
		die(err)
	}
	b, _ := json.MarshalIndent(map[string]any{"rendered": out, "paths": paths, "dry_run": true, "installed": false}, "", "  ")
	fmt.Println(string(b))
}

func appendParent(parents []string, id string) []string {
	if id == "" {
		return parents
	}
	for _, p := range parents {
		if p == id {
			return parents
		}
	}
	return append(parents, id)
}

func readCaptureInput(eventFile string, stdin bool) ([]byte, error) {
	var b []byte
	var err error
	if stdin {
		b, err = io.ReadAll(io.LimitReader(os.Stdin, maxCaptureInput+1))
	} else {
		if strings.TrimSpace(eventFile) == "" {
			return nil, fmt.Errorf("--event is required")
		}
		b, err = core.ReadRegularFileLimited(eventFile, maxCaptureInput)
	}
	if err != nil {
		return nil, err
	}
	if len(b) > maxCaptureInput {
		return nil, fmt.Errorf("capture input exceeds %d bytes", maxCaptureInput)
	}
	return b, nil
}

func adapterIngest(args []string) {
	fs := flag.NewFlagSet("adapter ingest", flag.ExitOnError)
	id := fs.String("adapter", "", "adapter id")
	dir := fs.String("dir", ".", "workspace directory")
	node := fs.String("node", "", "existing Session Tree node id")
	eventFile := fs.String("event", "", "hook event JSON file")
	stdin := fs.Bool("stdin", false, "read hook event JSON from stdin")
	source := fs.String("source", "fixture", "fixture or hook_unverified")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	if *id == "" || *dir == "" || *node == "" {
		die(fmt.Errorf("--adapter, --dir and --node are required"))
	}
	if *source != "fixture" && *source != "hook_unverified" {
		die(fmt.Errorf("--source must be fixture or hook_unverified"))
	}
	if (*eventFile == "") == !*stdin {
		die(fmt.Errorf("exactly one of --event or --stdin is required"))
	}
	input, err := readCaptureInput(*eventFile, *stdin)
	if err != nil {
		die(err)
	}
	result, err := adapter.Capture(*id, input)
	if err != nil {
		die(err)
	}
	if result.SessionRef == "" {
		die(fmt.Errorf("capture has no session_id; explicit session mapping is required"))
	}
	sessionID := adapter.SessionID(*id, result.SessionRef)
	key, err := adapter.IdempotencyKey(*id, *node, *source, result)
	if err != nil {
		die(err)
	}
	ending := adapter.IsSessionEnd(result.Event)
	var out core.Event
	created := false
	_, err = openStore(*dir).Transact(func(tx *core.Tx) error {
		if existing, ok := tx.FindIdempotent(key); ok {
			out = existing
			created = false
			return nil
		}
		n, ok := tx.Tree().Nodes[*node]
		if !ok {
			return fmt.Errorf("mapping node %s not found", *node)
		}
		if n.Deleted {
			return fmt.Errorf("mapping node %s is tombstoned", *node)
		}
		parents := appendParent(nil, n.LastEventID)
		if ending {
			sess, ok := tx.Tree().Sessions[sessionID]
			if !ok {
				return fmt.Errorf("session %s has no observed event; refusing session_end before session_seen", sessionID)
			}
			if sess.Ended {
				return fmt.Errorf("session %s is already ended", sessionID)
			}
			parents = appendParent(parents, sess.LastEventID)
		}
		payload := core.AdapterSessionPayload{
			SessionID: sessionID, SessionRef: result.SessionRef, NodeID: *node,
			Adapter: *id, HookEvent: result.Event, Tool: result.Tool, CwdRef: result.CwdRef,
			Timestamp: result.Timestamp, Source: *source, TrustLevel: "unverified_adapter",
		}
		pb, _ := json.Marshal(payload)
		typ := core.EvAdapterSessionSeen
		if ending {
			typ = core.EvAdapterSessionEnded
		}
		e, err := localEvent(*dir, core.Event{Type: typ, IdempotencyKey: key, CausalParents: parents, Payload: pb})
		if err != nil {
			return err
		}
		out, err = tx.Add(e)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		die(err)
	}
	summary := map[string]any{
		"adapter": *id, "event": result.Event, "event_type": out.Type,
		"event_id": out.EventID, "created": created, "idempotent_reuse": !created,
		"session_id": sessionID, "session_ref": result.SessionRef, "node_id": *node,
		"source": *source, "trust_level": "unverified_adapter",
		"session_end_is_task_done": false, "auto_install": false, "upload": false,
	}
	if *asJSON {
		b, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("ingested %s session=%s node=%s created=%v event=%s\n", result.Event, sessionID, *node, created, out.EventID)
}

func adapterCapture(args []string) {
	fs := flag.NewFlagSet("adapter capture", flag.ExitOnError)
	id := fs.String("adapter", "", "adapter id")
	eventFile := fs.String("event", "", "hook event JSON file")
	stdin := fs.Bool("stdin", false, "read hook event JSON from stdin")
	outFile := fs.String("out", "", "explicit output file")
	printOut := fs.Bool("print", false, "print normalized capture")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--adapter is required"))
	}
	if (*eventFile == "") == !*stdin {
		die(fmt.Errorf("exactly one of --event or --stdin is required"))
	}
	if (*outFile == "") == !*printOut {
		die(fmt.Errorf("exactly one of --out or --print is required"))
	}
	input, err := readCaptureInput(*eventFile, *stdin)
	if err != nil {
		die(err)
	}
	result, err := adapter.Capture(*id, input)
	if err != nil {
		die(err)
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	b = append(b, '\n')
	if *printOut {
		_, _ = os.Stdout.Write(b)
		return
	}
	if strings.TrimSpace(*outFile) == "" {
		die(fmt.Errorf("--out is required"))
	}
	f, err := os.OpenFile(*outFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		die(err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		die(err)
	}
	if err := f.Close(); err != nil {
		die(err)
	}
	fmt.Printf("capture metadata written to %s (no content captured)\n", *outFile)
}
