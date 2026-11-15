package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
)

type manifest struct {
	SchemaVersion int    `json:"schema_version"`
	WorkspaceID   string `json:"workspace_id"`
	DeviceID      string `json:"device_id"`
	CreatedAt     string `json:"created_at"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		cmdInit(os.Args[2:])
	case "event":
		cmdEvent(os.Args[2:])
	case "node":
		cmdNode(os.Args[2:])
	case "status", "replay":
		cmdStatus(os.Args[2:])
	case "doctor":
		cmdDoctor(os.Args[2:])
	case "ownership":
		cmdOwnership(os.Args[2:])
	case "note":
		cmdNote(os.Args[2:])
	case "handoff":
		cmdHandoff(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Anchor core (first local segment)
  anchor init --dir DIR --workspace ID --device ID
  anchor event append --dir DIR --file EVENT.json
  anchor node create --dir DIR --id ID --type goal|decision|task|branch|question|note --title T [--parent ID]
  anchor node edit --dir DIR --id ID --title T
  anchor node tombstone --dir DIR --id ID
  anchor node transition --dir DIR --id ID --to STATUS
  anchor note set --dir DIR --id ID --text TEXT
  anchor ownership takeover --dir DIR --id ID --owner NAME
  anchor handoff prepare --dir DIR --repo REPO
  anchor handoff restore --dir DIR --repo REPO --packet PACKET.json
  anchor status --dir DIR --repo REPO [--json]
  anchor doctor --dir DIR --repo REPO [--json]`)
}

func die(err error) { fmt.Fprintln(os.Stderr, "anchor:", err); os.Exit(1) }

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	ws := fs.String("workspace", "", "workspace id")
	dev := fs.String("device", "", "device id")
	_ = fs.Parse(args)
	if *ws == "" || *dev == "" {
		die(fmt.Errorf("--workspace and --device are required"))
	}
	if _, err := os.Stat(filepath.Join(*dir, "manifest.json")); err == nil {
		die(fmt.Errorf("workspace already initialized at %s", *dir))
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		die(err)
	}
	if _, err := core.Open(*dir); err != nil {
		die(err)
	}
	m := manifest{SchemaVersion: core.SchemaVersion, WorkspaceID: *ws, DeviceID: *dev, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(*dir, "manifest.json"), b, 0o600); err != nil {
		die(err)
	}
	fmt.Printf("initialized %s (workspace %s, device %s)\n", *dir, *ws, *dev)
}

func loadManifest(dir string) manifest {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		die(err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		die(err)
	}
	return m
}

func appendEvent(dir string, e core.Event) (core.Event, bool) {
	m := loadManifest(dir)
	if e.WorkspaceID == "" {
		e.WorkspaceID = m.WorkspaceID
	}
	if e.DeviceID == "" {
		e.DeviceID = m.DeviceID
	}
	s, err := core.Open(dir)
	if err != nil {
		die(err)
	}
	stored, created, err := s.Append(e)
	if err != nil {
		die(err)
	}
	return stored, created
}

func replayDir(dir string) core.ReplayResult {
	s, err := core.Open(dir)
	if err != nil {
		die(err)
	}
	events, err := s.Load()
	if err != nil {
		die(err)
	}
	return core.Replay(events)
}

func cmdEvent(args []string) {
	if len(args) < 1 || args[0] != "append" {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("event append", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	file := fs.String("file", "", "event JSON file")
	_ = fs.Parse(args[1:])
	if *file == "" {
		die(fmt.Errorf("--file is required"))
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		die(err)
	}
	var e core.Event
	if err := json.Unmarshal(b, &e); err != nil {
		die(err)
	}
	stored, created := appendEvent(*dir, e)
	fmt.Printf("%s %s created=%v\n", stored.EventID, stored.Type, created)
}

func cmdNode(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		nodeCreate(args[1:])
	case "transition":
		nodeTransition(args[1:])
	case "edit":
		nodeEdit(args[1:])
	case "tombstone":
		nodeTombstone(args[1:])
	default:
		usage()
		os.Exit(2)
	}
}

func nodeCreate(args []string) {
	fs := flag.NewFlagSet("node create", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	typ := fs.String("type", "", "node type")
	title := fs.String("title", "", "title")
	parent := fs.String("parent", "", "parent node id")
	owner := fs.String("owner", "local", "owner")
	_ = fs.Parse(args)
	if *id == "" || *typ == "" || *title == "" {
		die(fmt.Errorf("--id, --type and --title are required"))
	}
	nt, ok := core.ValidNodeType(*typ)
	if !ok {
		die(fmt.Errorf("invalid node type %q", *typ))
	}
	r := replayDir(*dir)
	if _, exists := r.Tree.Nodes[*id]; exists {
		die(fmt.Errorf("node %s already exists", *id))
	}
	if *parent != "" {
		if _, ok := r.Tree.Nodes[*parent]; !ok {
			die(fmt.Errorf("parent %s not found", *parent))
		}
	}
	lease := "lease_" + *id
	fencing := uint64(1)
	op := core.OwnershipPayload{NodeID: *id, Owner: *owner, LeaseID: lease, FencingToken: fencing, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	pb, _ := json.Marshal(op)
	claim, _ := appendEvent(*dir, core.Event{Type: core.EvOwnershipClaimed, CausalParents: []string{}, LeaseID: lease, FencingToken: fencing, Payload: pb})
	np := core.NodePayload{NodeID: *id, Type: string(nt), Title: *title, ParentID: *parent, Owner: *owner}
	nb, _ := json.Marshal(np)
	created, _ := appendEvent(*dir, core.Event{Type: core.EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: lease, FencingToken: fencing, Payload: nb})
	fmt.Printf("%s %s status=%s lease=%s\n", *id, nt, core.InitialStatus(nt), lease)
	_ = created
}

func nodeTransition(args []string) {
	fs := flag.NewFlagSet("node transition", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	to := fs.String("to", "", "target status")
	_ = fs.Parse(args)
	if *id == "" || *to == "" {
		die(fmt.Errorf("--id and --to are required"))
	}
	r := replayDir(*dir)
	n, ok := r.Tree.Nodes[*id]
	if !ok {
		die(fmt.Errorf("node %s not found", *id))
	}
	status := core.Status(*to)
	if !core.CanTransition(n.Type, n.Status, status) {
		die(fmt.Errorf("illegal transition %s: %s -> %s", n.Type, n.Status, status))
	}
	lease := n.LeaseID
	fencing := n.FencingToken
	owner := n.Owner
	if lease == "" || n.LeaseEventID == "" {
		die(fmt.Errorf("node %s has no ownership lease event", *id))
	}
	_ = owner
	sp := core.NodePayload{NodeID: *id, Status: string(status)}
	sb, _ := json.Marshal(sp)
	ev, _ := appendEvent(*dir, core.Event{Type: core.EvStatusChanged, CausalParents: []string{n.LeaseEventID}, BaseRevision: n.Revision, ExpectedRevision: n.Revision + 1, LeaseID: lease, FencingToken: fencing, Payload: sb})
	fmt.Printf("%s %s -> %s (%s)\n", *id, n.Status, status, ev.EventID)
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", ".", "git working tree for recovery evaluation")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	r := replayDir(*dir)
	var packet *handoff.Packet
	if p, _, err := handoff.Latest(*dir); err == nil {
		packet = &p
	}
	eval := handoff.Evaluate(*dir, *repo, packet)
	if *asJSON {
		out := map[string]any{
			"projection_status": r.RecoveryStatus, "recovery_status": eval.Status,
			"recovery_reasons": eval.Reasons, "handoff_id": eval.PacketID,
			"tree": r.Tree, "applied": r.Applied, "pending": r.Pending, "conflicts": r.Conflicts,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("projection_status: %s\n", r.RecoveryStatus)
	fmt.Printf("recovery_status: %s (a projection alone is not a complete recovery)\n", eval.Status)
	for _, reason := range eval.Reasons {
		fmt.Printf("  reason: %s\n", reason)
	}
	for _, n := range r.Tree.SortedNodes() {
		fmt.Printf("%-24s %-9s %-12s owner=%s rev=%d\n", n.ID, n.Type, n.Status, n.Owner, n.Revision)
	}
	for _, c := range r.Conflicts {
		fmt.Printf("conflict %s node=%s: %s\n", c.EventID, c.NodeID, c.Reason)
	}
	for _, p := range r.Pending {
		fmt.Printf("pending %s (waiting for causal parent/ownership)\n", p)
	}
}

func appendOwnedMutation(dir, id string, typ core.EventType, payload any) core.Event {
	r := replayDir(dir)
	n, ok := r.Tree.Nodes[id]
	if !ok {
		die(fmt.Errorf("node %s not found", id))
	}
	if n.LeaseID == "" || n.LeaseEventID == "" {
		die(fmt.Errorf("node %s has no ownership lease event", id))
	}
	pb, _ := json.Marshal(payload)
	ev, _ := appendEvent(dir, core.Event{Type: typ, CausalParents: []string{n.LeaseEventID}, BaseRevision: n.Revision, ExpectedRevision: n.Revision + 1, LeaseID: n.LeaseID, FencingToken: n.FencingToken, Payload: pb})
	return ev
}

func nodeEdit(args []string) {
	fs := flag.NewFlagSet("node edit", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	title := fs.String("title", "", "new title")
	_ = fs.Parse(args)
	if *id == "" || *title == "" {
		die(fmt.Errorf("--id and --title are required"))
	}
	ev := appendOwnedMutation(*dir, *id, core.EvNodeUpdated, core.NodePayload{NodeID: *id, Title: *title})
	fmt.Printf("edited %s (%s)\n", *id, ev.EventID)
}

func nodeTombstone(args []string) {
	fs := flag.NewFlagSet("node tombstone", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--id is required"))
	}
	ev := appendOwnedMutation(*dir, *id, core.EvNodeTombstoned, core.NodePayload{NodeID: *id})
	fmt.Printf("tombstoned %s (%s)\n", *id, ev.EventID)
}

func cmdNote(args []string) {
	if len(args) < 1 || args[0] != "set" {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("note set", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "note id")
	text := fs.String("text", "", "note text")
	_ = fs.Parse(args[1:])
	if *id == "" {
		die(fmt.Errorf("--id is required"))
	}
	ev := appendOwnedMutation(*dir, *id, core.EvNoteUpdated, core.NodePayload{NodeID: *id, Note: *text})
	fmt.Printf("note updated %s (%s)\n", *id, ev.EventID)
}

func cmdOwnership(args []string) {
	if len(args) < 1 || args[0] != "takeover" {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("ownership takeover", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	owner := fs.String("owner", "agent", "new owner")
	_ = fs.Parse(args[1:])
	if *id == "" {
		die(fmt.Errorf("--id is required"))
	}
	r := replayDir(*dir)
	n, ok := r.Tree.Nodes[*id]
	if !ok {
		die(fmt.Errorf("node %s not found", *id))
	}
	if n.LeaseEventID == "" {
		die(fmt.Errorf("node %s has no ownership lease event", *id))
	}
	token := n.FencingToken + 1
	lease := fmt.Sprintf("lease_%s_%d", *id, token)
	hp := core.OwnershipPayload{NodeID: *id, Owner: *owner, LeaseID: lease, FencingToken: token, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	hb, _ := json.Marshal(hp)
	ev, _ := appendEvent(*dir, core.Event{Type: core.EvOwnershipTakenOver, CausalParents: []string{n.LeaseEventID}, LeaseID: lease, FencingToken: token, Payload: hb})
	fmt.Printf("takeover %s owner=%s lease=%s token=%d (%s)\n", *id, *owner, lease, token, ev.EventID)
}

func cmdHandoff(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "prepare":
		fs := flag.NewFlagSet("handoff prepare", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace directory")
		repo := fs.String("repo", ".", "git working tree")
		_ = fs.Parse(args[1:])
		p, path, err := handoff.Prepare(*dir, *repo)
		if err != nil {
			die(err)
		}
		out := map[string]any{"handoff_id": p.HandoffID, "path": path, "recovery_status": p.RecoveryStatus, "artifact_availability": p.ArtifactAvailability, "warnings": p.Warnings, "content_hash": p.ContentHash}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "restore":
		fs := flag.NewFlagSet("handoff restore", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace directory")
		repo := fs.String("repo", ".", "git working tree")
		packet := fs.String("packet", "", "handoff packet path")
		_ = fs.Parse(args[1:])
		if *packet == "" {
			die(fmt.Errorf("--packet is required"))
		}
		p, err := handoff.Load(*packet)
		if err != nil {
			die(err)
		}
		eval := handoff.Evaluate(*dir, *repo, &p)
		b, _ := json.MarshalIndent(map[string]any{"handoff_id": p.HandoffID, "recovery_status": eval.Status, "reasons": eval.Reasons, "apply": "disabled: restore never overwrites code or executes commands"}, "", "  ")
		fmt.Println(string(b))
		if eval.Status != handoff.StatusComplete {
			os.Exit(1)
		}
	case "status":
		fs := flag.NewFlagSet("handoff status", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace directory")
		repo := fs.String("repo", ".", "git working tree")
		_ = fs.Parse(args[1:])
		p, path, err := handoff.Latest(*dir)
		if err != nil {
			fmt.Printf("recovery_status: incomplete\n  reason: no prepared handoff\n")
			return
		}
		eval := handoff.Evaluate(*dir, *repo, &p)
		b, _ := json.MarshalIndent(map[string]any{"handoff_id": p.HandoffID, "path": path, "recovery_status": eval.Status, "reasons": eval.Reasons}, "", "  ")
		fmt.Println(string(b))
	default:
		usage()
		os.Exit(2)
	}
}

func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", ".", "git working tree")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	checks := []map[string]any{}
	hardFail := false
	checks = append(checks, map[string]any{"check": "manifest", "status": "ok"})
	if _, err := os.Stat(filepath.Join(*dir, "manifest.json")); err != nil {
		checks[len(checks)-1] = map[string]any{"check": "manifest", "status": "fail", "detail": err.Error()}
		hardFail = true
	}
	r := replayDir(*dir)
	checks = append(checks, map[string]any{"check": "events", "status": "ok", "applied": len(r.Applied), "pending": len(r.Pending), "conflicts": len(r.Conflicts)})
	p, _, err := handoff.Latest(*dir)
	eval := handoff.Evaluation{Status: handoff.StatusIncomplete, Reasons: []string{"no prepared handoff"}}
	if err == nil {
		eval = handoff.Evaluate(*dir, *repo, &p)
	}
	checks = append(checks, map[string]any{"check": "recovery", "status": eval.Status, "reasons": eval.Reasons, "handoff_id": eval.PacketID})
	out := map[string]any{"checks": checks, "projection_status": r.RecoveryStatus, "recovery_status": eval.Status, "recovery_is_not_implied_by_projection": true}
	if *asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("projection_status: %s\nrecovery_status: %s\n", r.RecoveryStatus, eval.Status)
		for _, c := range checks {
			fmt.Printf("%v\n", c)
		}
	}
	if hardFail {
		os.Exit(1)
	}
}
