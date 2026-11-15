package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/adapter"
	"github.com/JingxuanC/anchor/internal/core"
	"github.com/JingxuanC/anchor/internal/handoff"
	"github.com/JingxuanC/anchor/internal/recovery"
)

type manifest struct {
	SchemaVersion   int    `json:"schema_version"`
	EventLogVersion int    `json:"event_log_version"`
	WorkspaceID     string `json:"workspace_id"`
	DeviceID        string `json:"device_id"`
	CreatedAt       string `json:"created_at"`
}

// stringList collects a repeatable flag value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
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
	case "evidence":
		cmdEvidence(os.Args[2:])
	case "handoff":
		cmdHandoff(os.Args[2:])
	case "adapter":
		cmdAdapter(os.Args[2:])
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
  anchor init --dir DIR --workspace ID --device ID [--attach]
  anchor event append --dir DIR --file EVENT.json
  anchor event audit --dir DIR [--json]
  anchor node create --dir DIR --id ID --type goal|decision|task|branch|question|note --title T [--parent ID]
      [--north-star T] [--must M]... [--must-not M]... [--success C]...
      [--decision D] [--rationale R] [--alternative A]... [--consequence C]...
      [--provenance agent_inferred|imported] [--confidence 0..1]
  anchor node edit --dir DIR --id ID [--title T] [--north-star T] [--must M]... [--must-not M]...
      [--success C]... [--decision D] [--rationale R] [--alternative A]... [--consequence C]...
      [--provenance agent_inferred|imported] [--confidence 0..1]
  anchor node tombstone --dir DIR --id ID
  anchor node transition --dir DIR --id ID --to STATUS
  anchor note set --dir DIR --id ID --text TEXT
  anchor evidence add --dir DIR --id EVID --node NODE --kind KIND --file PATH [--sha256 HEX64]
  anchor evidence audit --dir DIR [--json]
  anchor ownership takeover --dir DIR --id ID --owner NAME
  anchor handoff prepare --dir DIR --repo REPO
  anchor handoff restore --dir DIR --repo REPO --packet PACKET.json [--ack | --verify]
      [--evidence ID]... [--force] [--verifier NAME] [--note TEXT]
  anchor handoff export --dir WS --repo REPO --out DIR [--include-bytes] [--include-code] [--json]
  anchor handoff apply-code --package DIR --repo DIR [--apply] [--force] [--json]
  anchor handoff import --package DIR --target DIR [--apply] [--json]
  anchor handoff status --dir DIR --repo REPO
  anchor adapter capabilities [--json]
  anchor adapter render --adapter ID (--target DIR | --print)
  anchor adapter capture --adapter ID (--event FILE | --stdin) (--out FILE | --print | --capture-dir DIR)
  anchor adapter detect [--json]
  anchor adapter install --adapter claude-code --dir DIR [--repo PROJECT]
      [--scope project|user] [--apply | --uninstall] [--anchor-path PATH] [--json]
  anchor adapter ingest --adapter ID --dir WS --node NODE (--event FILE | --stdin) [--source fixture|hook_unverified] [--json]
  anchor status --dir DIR --repo REPO [--json]
  anchor doctor --dir DIR --repo REPO [--json]`)
}

func die(err error) { fmt.Fprintln(os.Stderr, "anchor:", err); os.Exit(1) }

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	ws := fs.String("workspace", "", "workspace id")
	dev := fs.String("device", "", "device id")
	attach := fs.Bool("attach", false, "adopt an existing project directory, storing state in <dir>/.anchor")
	_ = fs.Parse(args)
	if *ws == "" || *dev == "" {
		die(fmt.Errorf("--workspace and --device are required"))
	}
	if *attach {
		project, err := filepath.Abs(*dir)
		if err != nil {
			die(err)
		}
		_, stateDir, err := core.InitAttached(project, *ws, *dev)
		if err != nil {
			die(err)
		}
		// The state root is canonical; its parent is the canonical project path.
		project = filepath.Dir(stateDir)
		fmt.Printf("attached %s (state %s, workspace %s, device %s)\n", project, stateDir, *ws, *dev)
		changed, err := ensureProjectIgnore(project)
		if err != nil {
			fmt.Fprintf(os.Stderr, "anchor: warning: could not update the project .gitignore: %v\n", err)
		} else if changed {
			fmt.Println("ignored .anchor/ in the project .gitignore (commit it to keep the working tree clean)")
		}
		return
	}
	if _, err := core.InitWorkspace(*dir, *ws, *dev); err != nil {
		die(err)
	}
	fmt.Printf("initialized %s (workspace %s, device %s)\n", *dir, *ws, *dev)
}

// insideGitWorkTree reports whether dir is inside a Git work tree, including a
// subdirectory of a monorepo where dir has no .git of its own.
func insideGitWorkTree(dir string) bool {
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// ensureProjectIgnore keeps attached state out of the project repository. It
// only ever appends a marked block to .gitignore and does nothing when the
// project is not a Git work tree.
func ensureProjectIgnore(project string) (bool, error) {
	if !insideGitWorkTree(project) {
		return false, nil
	}
	path := filepath.Join(project, ".gitignore")
	if info, err := os.Lstat(path); err == nil {
		// Never write through a symlink or into something that is not a regular
		// file: attach must not touch anything outside the project tree.
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("refusing to modify %s: not a regular file", path)
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		switch strings.TrimSpace(line) {
		case ".anchor/", "/.anchor/", ".anchor", "/.anchor":
			return false, nil
		}
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("# Anchor workspace state (device-local; not part of the project repo)\n.anchor/\n")
	return true, os.WriteFile(path, []byte(b.String()), 0o644)
}

func readManifest(dir string) (manifest, error) {
	stateDir, err := core.StateDir(dir)
	if err != nil {
		return manifest{}, err
	}
	b, err := os.ReadFile(filepath.Join(stateDir, "manifest.json"))
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

func localEvent(dir string, e core.Event) (core.Event, error) {
	m, err := readManifest(dir)
	if err != nil {
		return core.Event{}, err
	}
	if e.WorkspaceID == "" {
		e.WorkspaceID = m.WorkspaceID
	} else if e.WorkspaceID != m.WorkspaceID {
		return core.Event{}, fmt.Errorf("event workspace_id %q does not match workspace %q", e.WorkspaceID, m.WorkspaceID)
	}
	if e.DeviceID == "" {
		e.DeviceID = m.DeviceID
	} else if e.DeviceID != m.DeviceID {
		return core.Event{}, fmt.Errorf("event device_id %q does not match local device %q", e.DeviceID, m.DeviceID)
	}
	return e, nil
}

func openStore(dir string) *core.Store {
	s, err := core.Open(dir)
	if err != nil {
		die(err)
	}
	return s
}

func appendEvent(dir string, e core.Event) (core.Event, bool) {
	e, err := localEvent(dir, e)
	if err != nil {
		die(err)
	}
	stored, created, err := openStore(dir).Append(e)
	if err != nil {
		die(err)
	}
	return stored, created
}

func quarantineDir(dir string) []core.Quarantine {
	s, err := core.Open(dir)
	if err != nil {
		return nil
	}
	r, err := s.LoadReport()
	if err != nil {
		return nil
	}
	return r.Quarantined
}

func replayDir(dir string) core.ReplayResult {
	s, err := core.Open(dir)
	if err != nil {
		die(err)
	}
	r, err := s.LoadReplay()
	if err != nil {
		die(err)
	}
	return r
}

func cmdEvent(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	if args[0] == "audit" {
		eventAudit(args[1:])
		return
	}
	if args[0] != "append" {
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
	// The successor gate also covers the raw event surface: a mutation event must
	// not slip past verification just because it was not issued by a node command.
	if e.Type.IsMutation() || e.Type == core.EvNodeCreated {
		requireHandoffCleared(*dir)
	}
	// Handoff progress events must come from `handoff restore`, which validates
	// the packet, the code state and the evidence artifacts.
	switch e.Type {
	case core.EvHandoffPrepared, core.EvHandoffAcked, core.EvHandoffVerified:
		die(fmt.Errorf("event type %s is recorded by the handoff commands, not by raw event append", e.Type))
	}
	stored, created := appendEvent(*dir, e)
	fmt.Printf("%s %s created=%v\n", stored.EventID, stored.Type, created)
}

func eventAudit(args []string) {
	fs := flag.NewFlagSet("event audit", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	a, err := openStore(*dir).AuditEventFormats()
	if err != nil {
		die(err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(a, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("read_only=%v writes=%v migration_dry_run_only=%v event_log_version=%d\n", a.ReadOnly, a.Writes, a.MigrationDryRunOnly, a.EventLogVersion)
	for _, f := range a.Files {
		fmt.Printf("%-40s format=%-22s events=%d %s\n", f.File, f.Format, f.Events, f.Reason)
	}
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
	northStar := fs.String("north-star", "", "goal north star (goal nodes only)")
	var must, mustNot, success stringList
	fs.Var(&must, "must", "goal must-constraint (repeatable; goal nodes only)")
	fs.Var(&mustNot, "must-not", "goal must-not constraint (repeatable; goal nodes only)")
	fs.Var(&success, "success", "goal success criterion (repeatable; goal nodes only)")
	decision := fs.String("decision", "", "decision taken (decision nodes only)")
	rationale := fs.String("rationale", "", "decision rationale (decision nodes only)")
	var alternatives, consequences stringList
	fs.Var(&alternatives, "alternative", "rejected alternative (repeatable; decision nodes only)")
	fs.Var(&consequences, "consequence", "decision consequence (repeatable; decision nodes only)")
	provenance := fs.String("provenance", string(core.ProvenanceAgentInferred), "provenance: agent_inferred|imported")
	confidence := fs.Float64("confidence", -1, "provenance confidence 0..1 (optional)")
	_ = fs.Parse(args)
	if *id == "" || *typ == "" || *title == "" {
		die(fmt.Errorf("--id, --type and --title are required"))
	}
	if err := checkProvenanceFlag(*provenance); err != nil {
		die(err)
	}
	if *confidence > 1 {
		die(fmt.Errorf("--confidence must be between 0 and 1"))
	}
	requireHandoffCleared(*dir)
	nt, ok := core.ValidNodeType(*typ)
	if !ok {
		die(fmt.Errorf("invalid node type %q", *typ))
	}
	lease := "lease_" + *id
	fencing := uint64(1)
	var created core.Event
	_, err := openStore(*dir).Transact(func(tx *core.Tx) error {
		if _, exists := tx.Tree().Nodes[*id]; exists {
			return fmt.Errorf("node %s already exists", *id)
		}
		if nt != core.NodeGoal && *parent == "" {
			return fmt.Errorf("non-goal node %s requires --parent", *id)
		}
		if *parent != "" {
			if _, ok := tx.Tree().Nodes[*parent]; !ok {
				return fmt.Errorf("parent %s not found", *parent)
			}
		}
		op := core.OwnershipPayload{NodeID: *id, Owner: *owner, LeaseID: lease, FencingToken: fencing, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		pb, _ := json.Marshal(op)
		ce, err := localEvent(*dir, core.Event{Type: core.EvOwnershipClaimed, CausalParents: []string{}, LeaseID: lease, FencingToken: fencing, Payload: pb})
		if err != nil {
			return err
		}
		claim, err := tx.Add(ce)
		if err != nil {
			return err
		}
		np := core.NodePayload{NodeID: *id, Type: string(nt), Title: *title, ParentID: *parent, Owner: *owner,
			NorthStar: *northStar, Must: must, MustNot: mustNot, SuccessCriteria: success,
			Decision: *decision, Rationale: *rationale, Alternatives: alternatives, Consequences: consequences,
			Provenance: *provenance}
		if *confidence >= 0 {
			np.Confidence = confidence
		}
		nb, _ := json.Marshal(np)
		ne, err := localEvent(*dir, core.Event{Type: core.EvNodeCreated, CausalParents: []string{claim.EventID}, LeaseID: lease, FencingToken: fencing, Payload: nb})
		if err != nil {
			return err
		}
		created, err = tx.Add(ne)
		return err
	})
	if err != nil {
		die(err)
	}
	fmt.Printf("%s %s status=%s lease=%s event=%s\n", *id, nt, core.InitialStatus(nt), lease, created.EventID)
}

func nodeTransition(args []string) {
	fs := flag.NewFlagSet("node transition", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	to := fs.String("to", "", "target status")
	evidence := fs.String("evidence", "", "comma-separated evidence ids (required for done)")
	userOverride := fs.Bool("user-override", false, "local user confirmation when no evidence is available")
	_ = fs.Parse(args)
	if *id == "" || *to == "" {
		die(fmt.Errorf("--id and --to are required"))
	}
	requireHandoffCleared(*dir)
	if *userOverride {
		die(fmt.Errorf("user_override is disabled: no trusted local attestation mechanism is available; use --evidence"))
	}
	status := core.Status(*to)
	var from core.Status
	var out core.Event
	_, err := openStore(*dir).Transact(func(tx *core.Tx) error {
		n, ok := tx.Tree().Nodes[*id]
		if !ok {
			return fmt.Errorf("node %s not found", *id)
		}
		if n.Deleted {
			return fmt.Errorf("node %s is tombstoned", *id)
		}
		from = n.Status
		if !core.CanTransition(n.Type, n.Status, status) {
			return fmt.Errorf("illegal transition %s: %s -> %s", n.Type, n.Status, status)
		}
		lease := n.LeaseID
		fencing := n.FencingToken
		if lease == "" || n.LeaseEventID == "" {
			return fmt.Errorf("node %s has no ownership lease event", *id)
		}
		var evidenceIDs []string
		if strings.TrimSpace(*evidence) != "" {
			for _, part := range strings.Split(*evidence, ",") {
				if v := strings.TrimSpace(part); v != "" {
					evidenceIDs = append(evidenceIDs, v)
				}
			}
		}
		if status == core.StatusDone {
			if len(evidenceIDs) == 0 {
				return fmt.Errorf("done requires --evidence id[,id]")
			}
			for _, evidenceID := range evidenceIDs {
				if _, ok := tx.Tree().Evidence[evidenceID]; !ok {
					return fmt.Errorf("evidence %q is unknown or its artifact failed verification", evidenceID)
				}
			}
		}
		sp := core.NodePayload{NodeID: *id, Status: string(status), EvidenceIDs: evidenceIDs}
		sb, _ := json.Marshal(sp)
		e, err := localEvent(*dir, core.Event{Type: core.EvStatusChanged, CausalParents: mutationParents(n), BaseRevision: n.Revision, ExpectedRevision: n.Revision + 1, LeaseID: lease, FencingToken: fencing, Payload: sb})
		if err != nil {
			return err
		}
		out, err = tx.Add(e)
		return err
	})
	if err != nil {
		die(err)
	}
	fmt.Printf("%s %s -> %s (%s)\n", *id, from, status, out.EventID)
}

func mutationParents(n *core.Node) []string {
	parents := []string{n.LastEventID}
	if n.LeaseEventID != "" && n.LeaseEventID != n.LastEventID {
		parents = append(parents, n.LeaseEventID)
	}
	return parents
}

func cmdEvidence(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	if args[0] == "audit" {
		evidenceAudit(args[1:])
		return
	}
	if args[0] != "add" {
		usage()
		os.Exit(2)
	}
	fs := flag.NewFlagSet("evidence add", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "evidence id")
	node := fs.String("node", "", "node id")
	kind := fs.String("kind", "test", "evidence kind")
	file := fs.String("file", "", "artifact file (explicit local path)")
	sha := fs.String("sha256", "", "expected 64 hex SHA-256")
	_ = fs.Parse(args[1:])
	if *id == "" || *node == "" || *file == "" {
		die(fmt.Errorf("--id, --node and --file are required"))
	}
	store := openStore(*dir)
	artifact, err := store.PutEvidenceFile(*file, *sha, []string{*id, *node, *kind})
	if err != nil {
		die(err)
	}
	var out core.Event
	_, err = store.Transact(func(tx *core.Tx) error {
		pb, _ := json.Marshal(core.EvidencePayload{EvidenceID: *id, NodeID: *node, Kind: *kind, SHA256: artifact.SHA256, Size: artifact.Size, ArtifactSchema: 1})
		e, err := localEvent(*dir, core.Event{Type: core.EvEvidenceAdded, Payload: pb})
		if err != nil {
			return err
		}
		out, err = tx.Add(e)
		return err
	})
	if err != nil {
		die(err)
	}
	fmt.Printf("evidence %s added sha256=%s size=%d (%s)\n", *id, artifact.SHA256, artifact.Size, out.EventID)
}

func evidenceAudit(args []string) {
	fs := flag.NewFlagSet("evidence audit", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	a, err := openStore(*dir).AuditEvidence()
	if err != nil {
		die(err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(a, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("read_only=%v gc_performed=%v referenced=%d blobs=%d orphan=%d invalid=%d legacy=%d\n",
		a.ReadOnly, a.GCPerformed, len(a.Referenced), len(a.BlobFiles), len(a.OrphanBlobs), len(a.InvalidArtifacts), len(a.LegacyEvidence))
	for _, id := range a.OrphanBlobs {
		fmt.Printf("orphan %s (not deleted)\n", id)
	}
	for _, issue := range a.InvalidArtifacts {
		fmt.Printf("invalid %s\n", issue)
	}
	for _, legacy := range a.LegacyEvidence {
		fmt.Printf("legacy %s\n", legacy)
	}
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", ".", "git working tree for recovery evaluation")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	stateDir, err := core.StateDir(*dir)
	if err != nil {
		die(err)
	}
	manifestInfo, _ := readManifest(*dir)
	r := replayDir(*dir)
	var packet *handoff.Packet
	if p, _, err := handoff.Latest(*dir); err == nil {
		packet = &p
	}
	eval := handoff.Evaluate(*dir, *repo, packet)
	gate, gateErr := handoff.Gate(*dir)
	if gateErr == nil && gate.Pending {
		eval.Reasons = append(eval.Reasons, "handoff "+gate.HandoffID+" is pending successor verification")
	}
	quarantined := quarantineDir(*dir)
	if len(quarantined) > 0 {
		if r.RecoveryStatus == "complete" {
			r.RecoveryStatus = "incomplete"
		}
		if eval.Status == handoff.StatusComplete {
			eval.Status = handoff.StatusIncomplete
		}
		eval.Reasons = append(eval.Reasons, "quarantined event files prevent a complete recovery")
	}
	if *asJSON {
		out := map[string]any{
			"workspace_dir": stateDir, "workspace_id": manifestInfo.WorkspaceID, "device_id": manifestInfo.DeviceID,
			"projection_status": r.RecoveryStatus, "recovery_status": eval.Status,
			"recovery_reasons": eval.Reasons, "handoff_id": eval.PacketID, "quarantined": quarantined,
			"tree": r.Tree, "applied": r.Applied, "pending": r.Pending, "conflicts": r.Conflicts,
			"handoff": gate,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("workspace_dir: %s\n", stateDir)
	fmt.Printf("projection_status: %s\n", r.RecoveryStatus)
	fmt.Printf("recovery_status: %s (a projection alone is not a complete recovery)\n", eval.Status)
	if gate.HandoffID != "" {
		fmt.Printf("handoff: id=%s acked=%v verified=%v pending=%v\n", gate.HandoffID, gate.Progress.Acked, gate.Progress.Verified, gate.Pending)
	}
	for _, reason := range eval.Reasons {
		fmt.Printf("  reason: %s\n", reason)
	}
	for _, n := range r.Tree.SortedNodes() {
		fmt.Printf("%-24s %-9s %-12s owner=%s rev=%d\n", n.ID, n.Type, n.Status, n.Owner, n.Revision)
	}
	for _, s := range r.Tree.SortedSessions() {
		fmt.Printf("session %-20s adapter=%s node=%s ended=%v trust=%s\n", s.ID, s.Adapter, s.NodeID, s.Ended, s.TrustLevel)
	}
	for _, c := range r.Conflicts {
		fmt.Printf("conflict %s node=%s: %s\n", c.EventID, c.NodeID, c.Reason)
	}
	for _, p := range r.Pending {
		fmt.Printf("pending %s (waiting for causal parent/ownership)\n", p)
	}
}

// appendHandoffEvent records a handoff ack/verify event.
func appendHandoffEvent(dir string, typ core.EventType, payload core.HandoffPayload) core.Event {
	var out core.Event
	_, err := openStore(dir).Transact(func(tx *core.Tx) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		e, err := localEvent(dir, core.Event{Type: typ, CausalParents: []string{}, Payload: b})
		if err != nil {
			return err
		}
		out, err = tx.Add(e)
		return err
	})
	if err != nil {
		die(err)
	}
	return out
}

// verifyEvidenceIDs makes sure the successor actually still has the artifacts it
// claims to have verified.
func verifyEvidenceIDs(dir string, ids []string) {
	if len(ids) == 0 {
		return
	}
	store := openStore(dir)
	replay, err := store.LoadReplay()
	if err != nil {
		die(err)
	}
	for _, id := range ids {
		entry, ok := replay.Tree.Evidence[id]
		if !ok {
			die(fmt.Errorf("verified evidence %s is unknown in this workspace", id))
		}
		if err := core.VerifyEvidenceArtifact(store.Dir, entry); err != nil {
			die(fmt.Errorf("verified evidence %s failed verification: %w", id, err))
		}
	}
}

// requireHandoffCleared is the successor gate: while the latest prepared handoff
// is unverified, mutations are refused so a successor cannot silently edit on
// top of an unexamined snapshot.
func requireHandoffCleared(dir string) {
	gate, err := handoff.Gate(dir)
	if err != nil {
		die(err)
	}
	if gate.Pending {
		if gate.PacketPath == "" || gate.PacketMissing {
			die(fmt.Errorf("handoff %s is pending successor verification but its packet file is missing; re-run 'anchor handoff prepare --dir %s --repo <repo>' and verify that packet before editing",
				gate.HandoffID, dir))
		}
		next := "--ack"
		if gate.Progress.Acked {
			next = "--verify"
		}
		die(fmt.Errorf("handoff %s is pending successor verification; run 'anchor handoff restore --dir %s --repo <repo> --packet %s %s' first (or verify it) before editing",
			gate.HandoffID, dir, gate.PacketPath, next))
	}
}

func appendOwnedMutation(dir, id string, typ core.EventType, payload any) core.Event {
	var out core.Event
	_, err := openStore(dir).Transact(func(tx *core.Tx) error {
		n, ok := tx.Tree().Nodes[id]
		if !ok {
			return fmt.Errorf("node %s not found", id)
		}
		if n.Deleted {
			return fmt.Errorf("node %s is tombstoned", id)
		}
		if n.LeaseID == "" || n.LeaseEventID == "" {
			return fmt.Errorf("node %s has no ownership lease event", id)
		}
		pb, _ := json.Marshal(payload)
		e, err := localEvent(dir, core.Event{Type: typ, CausalParents: mutationParents(n), BaseRevision: n.Revision, ExpectedRevision: n.Revision + 1, LeaseID: n.LeaseID, FencingToken: n.FencingToken, Payload: pb})
		if err != nil {
			return err
		}
		out, err = tx.Add(e)
		return err
	})
	if err != nil {
		die(err)
	}
	return out
}

func nodeEdit(args []string) {
	fs := flag.NewFlagSet("node edit", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	title := fs.String("title", "", "new title")
	northStar := fs.String("north-star", "", "goal north star (goal nodes only)")
	var must, mustNot, success stringList
	fs.Var(&must, "must", "goal must-constraint (repeatable; goal nodes only)")
	fs.Var(&mustNot, "must-not", "goal must-not constraint (repeatable; goal nodes only)")
	fs.Var(&success, "success", "goal success criterion (repeatable; goal nodes only)")
	decision := fs.String("decision", "", "decision taken (decision nodes only)")
	rationale := fs.String("rationale", "", "decision rationale (decision nodes only)")
	var alternatives, consequences stringList
	fs.Var(&alternatives, "alternative", "rejected alternative (repeatable; decision nodes only)")
	fs.Var(&consequences, "consequence", "decision consequence (repeatable; decision nodes only)")
	provenance := fs.String("provenance", "", "provenance: agent_inferred|imported")
	confidence := fs.Float64("confidence", -1, "provenance confidence 0..1 (optional)")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--id is required"))
	}
	requireHandoffCleared(*dir)
	if err := checkProvenanceFlag(*provenance); err != nil {
		die(err)
	}
	if *confidence > 1 {
		die(fmt.Errorf("--confidence must be between 0 and 1"))
	}
	np := core.NodePayload{NodeID: *id, Title: *title,
		NorthStar: *northStar, Must: must, MustNot: mustNot, SuccessCriteria: success,
		Decision: *decision, Rationale: *rationale, Alternatives: alternatives, Consequences: consequences,
		Provenance: *provenance}
	if *confidence >= 0 {
		np.Confidence = confidence
	}
	changed := *title != "" || *northStar != "" || *decision != "" || *rationale != "" || *provenance != "" ||
		*confidence >= 0 || len(must) > 0 || len(mustNot) > 0 || len(success) > 0 || len(alternatives) > 0 || len(consequences) > 0
	if !changed {
		die(fmt.Errorf("no changes requested; pass at least one field flag"))
	}
	ev := appendOwnedMutation(*dir, *id, core.EvNodeUpdated, np)
	fmt.Printf("edited %s (%s)\n", *id, ev.EventID)
}

// checkProvenanceFlag keeps agents from self-reporting user confirmation.
func checkProvenanceFlag(p string) error {
	if !core.ValidProvenance(p) {
		return fmt.Errorf("invalid --provenance %q: use agent_inferred or imported", p)
	}
	if core.Provenance(p) == core.ProvenanceUserConfirmed {
		return fmt.Errorf("--provenance user_confirmed requires a local user attestation; agents may only record agent_inferred or imported")
	}
	return nil
}

func nodeTombstone(args []string) {
	fs := flag.NewFlagSet("node tombstone", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	id := fs.String("id", "", "node id")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("--id is required"))
	}
	requireHandoffCleared(*dir)
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
	requireHandoffCleared(*dir)
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
	requireHandoffCleared(*dir)
	var out core.Event
	var token uint64
	var lease string
	_, err := openStore(*dir).Transact(func(tx *core.Tx) error {
		n, ok := tx.Tree().Nodes[*id]
		if !ok {
			return fmt.Errorf("node %s not found", *id)
		}
		if n.LeaseEventID == "" {
			return fmt.Errorf("node %s has no ownership lease event", *id)
		}
		token = n.FencingToken + 1
		lease = fmt.Sprintf("lease_%s_%d", *id, token)
		hp := core.OwnershipPayload{NodeID: *id, Owner: *owner, LeaseID: lease, FencingToken: token, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		hb, _ := json.Marshal(hp)
		e, err := localEvent(*dir, core.Event{Type: core.EvOwnershipTakenOver, CausalParents: []string{n.LeaseEventID}, LeaseID: lease, FencingToken: token, Payload: hb})
		if err != nil {
			return err
		}
		out, err = tx.Add(e)
		return err
	})
	if err != nil {
		die(err)
	}
	fmt.Printf("takeover %s owner=%s lease=%s token=%d (%s)\n", *id, *owner, lease, token, out.EventID)
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
		ack := fs.Bool("ack", false, "record successor acknowledgement of the packet")
		verify := fs.Bool("verify", false, "record successor verification of the packet")
		var evidence stringList
		fs.Var(&evidence, "evidence", "evidence id the successor re-verified (repeatable, --verify only)")
		force := fs.Bool("force", false, "record the event despite blockers (recorded as forced)")
		verifier := fs.String("verifier", "local", "identity recorded on the event")
		note := fs.String("note", "", "free-form note recorded with the event")
		_ = fs.Parse(args[1:])
		if *packet == "" {
			die(fmt.Errorf("--packet is required"))
		}
		if *ack && *verify {
			die(fmt.Errorf("--ack and --verify are mutually exclusive"))
		}
		p, err := handoff.Load(*packet)
		if err != nil {
			die(err)
		}
		if local, err := readManifest(*dir); err == nil && p.Tree.WorkspaceID != "" && local.WorkspaceID != "" && p.Tree.WorkspaceID != local.WorkspaceID {
			die(fmt.Errorf("packet belongs to workspace %q, not this workspace %q", p.Tree.WorkspaceID, local.WorkspaceID))
		}
		gate, err := handoff.Gate(*dir)
		if err != nil {
			die(err)
		}
		if gate.HandoffID != "" && gate.HandoffID != p.HandoffID && (*ack || *verify) {
			die(fmt.Errorf("packet %s is not the latest prepared handoff (%s); re-run 'anchor handoff prepare' and verify the new packet", p.HandoffID, gate.HandoffID))
		}
		plan, err := handoff.BuildPlan(*dir, *repo, p, *packet)
		if err != nil {
			die(err)
		}
		if *ack {
			if plan.Progress.Acked && !*force {
				die(fmt.Errorf("handoff %s is already acked", p.HandoffID))
			}
			if !plan.CanAck && !*force {
				die(fmt.Errorf("cannot acknowledge handoff %s: %s", p.HandoffID, strings.Join(plan.AckBlockers, "; ")))
			}
			payload := core.HandoffPayload{HandoffID: p.HandoffID, ContentHash: p.ContentHash, CodeHead: plan.Code.ActualHead,
				Verifier: *verifier, Forced: *force || !plan.CanAck, Note: *note}
			ev := appendHandoffEvent(*dir, core.EvHandoffAcked, payload)
			b, _ := json.MarshalIndent(map[string]any{"handoff_id": p.HandoffID, "acked": true, "forced": payload.Forced, "event": ev.EventID}, "", "  ")
			fmt.Println(string(b))
			return
		}
		if *verify {
			if !plan.Progress.Acked {
				die(fmt.Errorf("handoff %s must be acked before verification", p.HandoffID))
			}
			if plan.Progress.Verified && !*force {
				die(fmt.Errorf("handoff %s is already verified", p.HandoffID))
			}
			if !plan.CanVerify && !*force {
				die(fmt.Errorf("cannot verify handoff %s: %s", p.HandoffID, strings.Join(plan.VerifyBlockers, "; ")))
			}
			verifyEvidenceIDs(*dir, evidence)
			payload := core.HandoffPayload{HandoffID: p.HandoffID, ContentHash: p.ContentHash, CodeHead: plan.Code.ActualHead,
				EvidenceIDs: evidence, Verifier: *verifier, Forced: *force || !plan.CanVerify, Note: *note}
			ev := appendHandoffEvent(*dir, core.EvHandoffVerified, payload)
			b, _ := json.MarshalIndent(map[string]any{"handoff_id": p.HandoffID, "verified": true, "forced": payload.Forced, "event": ev.EventID}, "", "  ")
			fmt.Println(string(b))
			return
		}
		b, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(b))
		if !plan.Progress.Verified {
			os.Exit(1)
		}
	case "export":
		fs := flag.NewFlagSet("handoff export", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace directory")
		repo := fs.String("repo", ".", "git working tree")
		out := fs.String("out", "", "explicit new export package directory")
		includeBytes := fs.Bool("include-bytes", false, "explicitly include selected local bytes")
		includeCode := fs.Bool("include-code", false, "include a verified git bundle of the prepared commit")
		asJSON := fs.Bool("json", false, "JSON output")
		_ = fs.Parse(args[1:])
		if *out == "" {
			die(fmt.Errorf("--out is required"))
		}
		m, path, err := recovery.ExportWithCode(*dir, *repo, *out, *includeBytes, *includeCode)
		if err != nil {
			die(err)
		}
		summary := map[string]any{"export_id": m.ExportID, "path": path, "workspace_id": m.WorkspaceID, "device_id": m.DeviceID, "artifact_availability": m.ArtifactAvailability, "recovery_status": m.RecoveryStatus, "code_apply": m.CodeApply, "adapter_native_recovery": m.AdapterNativeRecovery, "code_commit_included": m.CodeCommitIncluded, "code_bundle_included": m.CodeBundleIncluded, "files": len(m.Files), "event_batches": m.EventBatches, "workspace_state_included": true, "evidence_bytes": m.EvidenceBytesIncluded, "untracked_bytes": m.UntrackedBytesIncluded, "auto_install": false, "upload": false, "warnings": m.Warnings}
		if *asJSON {
			b, _ := json.MarshalIndent(summary, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Printf("exported %s to %s (recovery=%s artifacts=%s files=%d event_batches=%d code_bundle=%v)\n", m.ExportID, path, m.RecoveryStatus, m.ArtifactAvailability, len(m.Files), m.EventBatches, m.CodeBundleIncluded)
	case "import":
		fs := flag.NewFlagSet("handoff import", flag.ExitOnError)
		pkg := fs.String("package", "", "export package directory")
		target := fs.String("target", "", "explicit new/empty target directory")
		apply := fs.Bool("apply", false, "write verified bytes; default is dry-run")
		asJSON := fs.Bool("json", false, "JSON output")
		_ = fs.Parse(args[1:])
		if *pkg == "" || *target == "" {
			die(fmt.Errorf("--package and --target are required"))
		}
		r, err := recovery.Import(*pkg, *target, !*apply)
		if err != nil {
			die(err)
		}
		if *asJSON {
			b, _ := json.MarshalIndent(r, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Printf("import dry_run=%v target=%s verified_files=%d workspace_rebuilt=%v event_batches=%d events_applied=%d projection=%s recovery=%s code_apply=%s writes=%d\n", r.DryRun, r.Target, r.FilesVerified, r.WorkspaceRebuilt, r.EventBatches, r.EventsApplied, r.ProjectionStatus, r.RecoveryStatus, r.CodeApply, r.Writes)
	case "apply-code":
		fs := flag.NewFlagSet("handoff apply-code", flag.ExitOnError)
		pkg := fs.String("package", "", "export package directory")
		repo := fs.String("repo", "", "git working tree to restore code into")
		apply := fs.Bool("apply", false, "apply the code bytes; default is a read-only plan")
		force := fs.Bool("force", false, "apply even if the target working tree is dirty (changes are stashed and a backup ref is created)")
		asJSON := fs.Bool("json", false, "JSON output")
		_ = fs.Parse(args[1:])
		if *pkg == "" || *repo == "" {
			die(fmt.Errorf("--package and --repo are required"))
		}
		r, err := recovery.ApplyCode(*pkg, *repo, !*apply, *force)
		if err != nil {
			die(err)
		}
		if *asJSON {
			b, _ := json.MarshalIndent(r, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Printf("apply-code dry_run=%v repo=%s packet_head=%s bundle=%v trusted=%v applied=%v backup_ref=%s untracked=%d\n",
				r.DryRun, r.Repo, r.PacketHead, r.BundleIncluded, r.BundleVerified, r.Applied, r.BackupRef, r.UntrackedWritten)
			for _, b := range r.Blockers {
				fmt.Printf("blocker: %s\n", b)
			}
			for _, w := range r.Warnings {
				fmt.Printf("warning: %s\n", w)
			}
		}
		if len(r.Blockers) > 0 || (!*apply && !r.DryRun) {
			os.Exit(1)
		}
	case "status":
		fs := flag.NewFlagSet("handoff status", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace directory")
		repo := fs.String("repo", ".", "git working tree")
		_ = fs.Parse(args[1:])
		gate, err := handoff.Gate(*dir)
		if err != nil {
			die(err)
		}
		out := map[string]any{"pending": gate.Pending, "packet_missing": gate.PacketMissing,
			"acked": gate.Progress.Acked, "verified": gate.Progress.Verified, "forced": gate.Progress.Forced}
		if gate.HandoffID != "" {
			out["handoff_id"] = gate.HandoffID
			out["path"] = gate.PacketPath
		}
		if p, path, err := handoff.Latest(*dir); err == nil {
			eval := handoff.Evaluate(*dir, *repo, &p)
			out["handoff_id"] = p.HandoffID
			out["path"] = path
			out["recovery_status"] = eval.Status
			out["reasons"] = eval.Reasons
		} else {
			out["recovery_status"] = "incomplete"
			out["reasons"] = []string{"no prepared handoff"}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
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
	stateDir := *dir
	resolvedDir, stateErr := core.StateDir(*dir)
	if stateErr == nil {
		stateDir = resolvedDir
	}
	if stateErr != nil {
		checks[len(checks)-1] = map[string]any{"check": "manifest", "status": "fail", "detail": stateErr.Error()}
		hardFail = true
	} else if _, err := os.Stat(filepath.Join(stateDir, "manifest.json")); err != nil {
		checks[len(checks)-1] = map[string]any{"check": "manifest", "status": "fail", "detail": err.Error()}
		hardFail = true
	}
	if hardFail {
		out := map[string]any{"checks": checks, "workspace_dir": stateDir, "projection_status": "incomplete", "recovery_status": "incomplete"}
		if *asJSON {
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		} else {
			for _, c := range checks {
				fmt.Printf("%v\n", c)
			}
		}
		os.Exit(1)
	}
	r := replayDir(*dir)
	q := quarantineDir(*dir)
	eventStatus := "ok"
	if len(q) > 0 {
		eventStatus = "warn"
	}
	checks = append(checks, map[string]any{"check": "events", "status": eventStatus, "applied": len(r.Applied), "pending": len(r.Pending), "conflicts": len(r.Conflicts), "quarantined": q})
	p, _, err := handoff.Latest(*dir)
	eval := handoff.Evaluation{Status: handoff.StatusIncomplete, Reasons: []string{"no prepared handoff"}}
	if err == nil {
		eval = handoff.Evaluate(*dir, *repo, &p)
	}
	checks = append(checks, map[string]any{"check": "recovery", "status": eval.Status, "reasons": eval.Reasons, "handoff_id": eval.PacketID})
	gate, gateErr := handoff.Gate(*dir)
	handoffStatus := "ok"
	var handoffDetail any
	if gateErr != nil {
		handoffStatus = "fail"
		handoffDetail = gateErr.Error()
	} else if gate.Pending {
		handoffStatus = "warn"
		handoffDetail = map[string]any{"handoff_id": gate.HandoffID, "acked": gate.Progress.Acked, "verified": gate.Progress.Verified}
	} else if gate.HandoffID != "" {
		handoffDetail = map[string]any{"handoff_id": gate.HandoffID, "acked": gate.Progress.Acked, "verified": gate.Progress.Verified}
	}
	check := map[string]any{"check": "handoff", "status": handoffStatus}
	if handoffDetail != nil {
		check["detail"] = handoffDetail
	}
	checks = append(checks, check)

	// Adapter check: real version detection plus the recorded hook installation.
	detections := adapter.DetectAll(0)
	adapterDetail := map[string]any{}
	anyInstalled := false
	for _, d := range detections {
		adapterDetail[d.AdapterID] = map[string]any{"installed": d.Installed, "version": d.Version, "hooks": d.NativeHookStatus, "end_to_end_verified": false}
		if d.Installed {
			anyInstalled = true
		}
	}
	if st, err := adapter.LoadInstallState(stateDir, "claude-code"); err != nil {
		adapterDetail["hook_install_error"] = err.Error()
	} else if st != nil {
		adapterDetail["installed_hooks"] = map[string]any{"adapter": st.AdapterID, "scope": st.Scope, "settings": st.SettingsPath, "applied_at": st.AppliedAt}
	}
	adapterStatus := "warn"
	if anyInstalled {
		adapterStatus = "ok"
	}
	checks = append(checks, map[string]any{"check": "adapters", "status": adapterStatus, "detail": adapterDetail})

	out := map[string]any{"checks": checks, "workspace_dir": stateDir, "projection_status": r.RecoveryStatus, "recovery_status": eval.Status, "recovery_is_not_implied_by_projection": true}
	if *asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("workspace_dir: %s\nprojection_status: %s\nrecovery_status: %s\n", stateDir, r.RecoveryStatus, eval.Status)
		for _, c := range checks {
			fmt.Printf("%v\n", c)
		}
	}
}
