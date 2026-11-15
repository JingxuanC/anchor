package handoff

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
)

const (
	StatusComplete   = "complete"
	StatusPartial    = "partial"
	StatusIncomplete = "incomplete"
	StatusStaleCode  = "stale_code"
)

type FileRef struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type CodeState struct {
	Branch             string    `json:"branch"`
	Head               string    `json:"head,omitempty"`
	Tree               string    `json:"tree,omitempty"`
	Dirty              bool      `json:"dirty"`
	StagedCount        int       `json:"staged_count"`
	UnstagedCount      int       `json:"unstaged_count"`
	UntrackedCount     int       `json:"untracked_count"`
	StagedDiffSHA256   string    `json:"staged_diff_sha256,omitempty"`
	UnstagedDiffSHA256 string    `json:"unstaged_diff_sha256,omitempty"`
	ChangedFiles       []string  `json:"changed_files,omitempty"`
	Untracked          []FileRef `json:"untracked,omitempty"`
}

type TreeSummary struct {
	WorkspaceID      string          `json:"workspace_id"`
	GoalIDs          []string        `json:"goal_ids,omitempty"`
	ActiveNodeID     string          `json:"active_node_id,omitempty"`
	Nodes            []core.Node     `json:"nodes"`
	Pending          []string        `json:"pending,omitempty"`
	Conflicts        []core.Conflict `json:"conflicts,omitempty"`
	ProjectionStatus string          `json:"projection_status"`
}

type Packet struct {
	SchemaVersion        int               `json:"schema_version"`
	HandoffID            string            `json:"handoff_id"`
	CreatedAt            time.Time         `json:"created_at"`
	Summary              string            `json:"summary"`
	Tree                 TreeSummary       `json:"tree"`
	CodeState            CodeState         `json:"code_state"`
	ArtifactAvailability string            `json:"artifact_availability"` // metadata_only | bytes_present | missing
	RecoveryStatus       string            `json:"recovery_status"`
	Warnings             []string          `json:"warnings,omitempty"`
	Quarantined          []core.Quarantine `json:"quarantined,omitempty"`
	ContentHash          string            `json:"content_hash,omitempty"`
}

type Evaluation struct {
	Status   string   `json:"recovery_status"`
	Reasons  []string `json:"reasons,omitempty"`
	PacketID string   `json:"handoff_id,omitempty"`
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("ho_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func git(repo string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", full...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func safeRel(p string) (string, bool) {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") || strings.Contains(p, ":") {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || part == "" {
			return "", false
		}
	}
	return p, true
}

func codeState(repo string) (CodeState, error) {
	branchB, err := git(repo, "symbolic-ref", "--short", "-q", "HEAD")
	branch := strings.TrimSpace(string(branchB))
	if err != nil || branch == "" {
		branch = "detached"
	}
	headB, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		return CodeState{}, fmt.Errorf("repository has no HEAD commit: %w", err)
	}
	head := strings.TrimSpace(string(headB))
	treeB, _ := git(repo, "rev-parse", "HEAD^{tree}")
	tree := strings.TrimSpace(string(treeB))
	statusB, err := git(repo, "status", "--porcelain=v1", "-z")
	if err != nil {
		return CodeState{}, err
	}
	parts := strings.Split(string(statusB), "\x00")
	var changed []string
	staged, unstaged := 0, 0
	for i := 0; i < len(parts); i++ {
		entry := parts[i]
		if len(entry) < 4 {
			continue
		}
		x, y := entry[0], entry[1]
		p := entry[3:]
		if p == "" {
			continue
		}
		if x != ' ' && x != '?' {
			staged++
		}
		if y != ' ' && y != '?' {
			unstaged++
		}
		if x == 'R' || x == 'C' {
			i++
		} // skip original path token
		if safe, ok := safeRel(p); ok {
			changed = append(changed, safe)
		}
	}
	untrackedB, err := git(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return CodeState{}, err
	}
	var untracked []FileRef
	for _, raw := range strings.Split(string(untrackedB), "\x00") {
		if raw == "" {
			continue
		}
		p, ok := safeRel(raw)
		if !ok {
			continue
		}
		info, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(p)))
		if err != nil || !info.Mode().IsRegular() {
			untracked = append(untracked, FileRef{Path: p, Skipped: true, Reason: "not a regular file"})
			continue
		}
		ref := FileRef{Path: p, Size: info.Size()}
		if info.Size() > 10<<20 {
			ref.Skipped = true
			ref.Reason = "size limit"
		} else {
			b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(p)))
			if err != nil {
				ref.Skipped = true
				ref.Reason = "read error"
			} else {
				ref.SHA256 = hashBytes(b)
			}
		}
		untracked = append(untracked, ref)
	}
	sort.Slice(untracked, func(i, j int) bool { return untracked[i].Path < untracked[j].Path })
	sort.Strings(changed)
	stagedDiff, _ := git(repo, "diff", "--cached", "--binary")
	unstagedDiff, _ := git(repo, "diff", "--binary")
	cs := CodeState{Branch: branch, Head: head, Tree: tree, StagedCount: staged, UnstagedCount: unstaged, UntrackedCount: len(untracked), ChangedFiles: changed, Untracked: untracked}
	if len(stagedDiff) > 0 {
		cs.StagedDiffSHA256 = hashBytes(stagedDiff)
	}
	if len(unstagedDiff) > 0 {
		cs.UnstagedDiffSHA256 = hashBytes(unstagedDiff)
	}
	cs.Dirty = len(changed) > 0 || len(untracked) > 0
	return cs, nil
}

func treeSummary(events []core.Event, result core.ReplayResult) TreeSummary {
	ts := TreeSummary{WorkspaceID: "", ProjectionStatus: result.RecoveryStatus, Pending: result.Pending, Conflicts: result.Conflicts}
	if len(events) > 0 {
		ts.WorkspaceID = events[0].WorkspaceID
	}
	for _, n := range result.Tree.SortedNodes() {
		ts.Nodes = append(ts.Nodes, *n)
		if n.Type == core.NodeGoal && !n.Deleted {
			ts.GoalIDs = append(ts.GoalIDs, n.ID)
		}
		if ts.ActiveNodeID == "" && (n.Status == core.StatusInProgress || n.Status == core.StatusActive) && !n.Deleted {
			ts.ActiveNodeID = n.ID
		}
	}
	return ts
}

func Prepare(dir, repo string) (Packet, string, error) {
	store, err := core.Open(dir)
	if err != nil {
		return Packet{}, "", err
	}
	report, err := store.LoadReport()
	if err != nil {
		return Packet{}, "", err
	}
	events := report.Events
	replay, err := store.LoadReplay()
	if err != nil {
		return Packet{}, "", err
	}
	cs, err := codeState(repo)
	if err != nil {
		return Packet{}, "", err
	}
	p := Packet{SchemaVersion: 1, HandoffID: newID(), CreatedAt: time.Now().UTC(),
		Summary: "Structured handoff snapshot. This summary is not a substitute for code and evidence verification.",
		Tree:    treeSummary(events, replay), CodeState: cs, ArtifactAvailability: "metadata_only", Quarantined: report.Quarantined}
	p.RecoveryStatus = StatusComplete
	if len(report.Quarantined) > 0 {
		p.RecoveryStatus = StatusIncomplete
		p.Warnings = append(p.Warnings, "some event files are quarantined; recovery is incomplete")
	}
	if replay.RecoveryStatus != "complete" {
		p.RecoveryStatus = StatusIncomplete
		p.Warnings = append(p.Warnings, "projection has pending events or conflicts")
	}
	if cs.Dirty {
		p.RecoveryStatus = StatusIncomplete
		p.Warnings = append(p.Warnings, "uncommitted code is metadata-only; artifact bytes were not captured")
	}
	if len(p.Warnings) == 0 {
		p.Warnings = nil
	}
	body, _ := json.Marshal(p)
	sum := sha256.Sum256(body)
	p.ContentHash = hex.EncodeToString(sum[:])
	b, _ := json.MarshalIndent(p, "", "  ")
	hd := filepath.Join(dir, "handoff")
	if err := os.MkdirAll(hd, 0o700); err != nil {
		return Packet{}, "", err
	}
	path := filepath.Join(hd, p.HandoffID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return Packet{}, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Packet{}, "", err
	}
	return p, path, nil
}

func Load(path string) (Packet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Packet{}, err
	}
	var p Packet
	if err := json.Unmarshal(b, &p); err != nil {
		return Packet{}, err
	}
	clone := p
	clone.ContentHash = ""
	body, _ := json.Marshal(clone)
	sum := sha256.Sum256(body)
	if p.ContentHash != hex.EncodeToString(sum[:]) {
		return Packet{}, fmt.Errorf("handoff content hash mismatch")
	}
	return p, nil
}

func Latest(dir string) (Packet, string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "handoff", "ho_*.json"))
	if err != nil {
		return Packet{}, "", err
	}
	if len(paths) == 0 {
		return Packet{}, "", os.ErrNotExist
	}
	sort.Strings(paths)
	for i := len(paths) - 1; i >= 0; i-- {
		p, err := Load(paths[i])
		if err == nil {
			return p, paths[i], nil
		}
	}
	return Packet{}, "", os.ErrNotExist
}

// Evaluate is read-only: it never applies code or executes restore commands.
func Evaluate(dir, repo string, p *Packet) Evaluation {
	ev := Evaluation{Status: StatusIncomplete}
	if p == nil {
		ev.Reasons = append(ev.Reasons, "no prepared handoff for the current workspace")
		return ev
	}
	ev.PacketID = p.HandoffID
	store, err := core.Open(dir)
	if err != nil {
		ev.Reasons = append(ev.Reasons, "cannot open workspace: "+err.Error())
		return ev
	}
	report, err := store.LoadReport()
	if err != nil {
		ev.Reasons = append(ev.Reasons, "cannot load events: "+err.Error())
		return ev
	}
	if len(report.Quarantined) > 0 {
		ev.Status = StatusIncomplete
		ev.Reasons = append(ev.Reasons, "some event files are quarantined; recovery is incomplete")
		return ev
	}
	replay, err := store.LoadReplay()
	if err != nil {
		ev.Reasons = append(ev.Reasons, "cannot replay events: "+err.Error())
		return ev
	}
	if replay.RecoveryStatus != "complete" {
		ev.Reasons = append(ev.Reasons, "projection is incomplete (pending/conflicts)")
	}
	cs, err := codeState(repo)
	if err != nil {
		ev.Reasons = append(ev.Reasons, err.Error())
		return ev
	}
	if cs.Branch != p.CodeState.Branch || cs.Head != p.CodeState.Head {
		ev.Status = StatusStaleCode
		ev.Reasons = append(ev.Reasons, "branch/HEAD differs from prepared handoff")
		return ev
	}
	if cs.StagedDiffSHA256 != p.CodeState.StagedDiffSHA256 || cs.UnstagedDiffSHA256 != p.CodeState.UnstagedDiffSHA256 {
		ev.Status = StatusStaleCode
		ev.Reasons = append(ev.Reasons, "working-tree diff differs from prepared handoff")
		return ev
	}
	if p.RecoveryStatus != StatusComplete {
		ev.Reasons = append(ev.Reasons, "prepared handoff is "+p.RecoveryStatus)
		return ev
	}
	if cs.Dirty {
		ev.Status = StatusIncomplete
		ev.Reasons = append(ev.Reasons, "uncommitted code has no captured bytes; restore is metadata-only")
		return ev
	}
	ev.Status = StatusComplete
	return ev
}

func CheckPath(path string) error {
	if _, ok := safeRel(path); !ok {
		return fmt.Errorf("unsafe path %q", path)
	}
	return nil
}
