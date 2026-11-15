package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
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
  anchor node transition --dir DIR --id ID --to STATUS
  anchor status --dir DIR [--json]
  anchor replay --dir DIR [--json]`)
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
	claim, _ := appendEvent(*dir, core.Event{Type: core.EvOwnershipClaimed, CausalParents: []string{}, Payload: pb})
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
	hp := core.OwnershipPayload{NodeID: *id, Owner: owner, LeaseID: lease, FencingToken: fencing, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	hb, _ := json.Marshal(hp)
	heartbeat, _ := appendEvent(*dir, core.Event{Type: core.EvOwnershipHeartbeat, CausalParents: []string{n.LeaseEventID}, LeaseID: lease, FencingToken: fencing, Payload: hb})
	sp := core.NodePayload{NodeID: *id, Status: string(status)}
	sb, _ := json.Marshal(sp)
	ev, _ := appendEvent(*dir, core.Event{Type: core.EvStatusChanged, CausalParents: []string{heartbeat.EventID}, BaseRevision: n.Revision, ExpectedRevision: n.Revision + 1, LeaseID: lease, FencingToken: fencing, Payload: sb})
	fmt.Printf("%s %s -> %s (%s)\n", *id, n.Status, status, ev.EventID)
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	r := replayDir(*dir)
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("recovery_status: %s\n", r.RecoveryStatus)
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
