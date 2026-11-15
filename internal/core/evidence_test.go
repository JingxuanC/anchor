package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArtifact(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func evidencePayload(id, node, digest string, size int64) json.RawMessage {
	b, _ := json.Marshal(EvidencePayload{EvidenceID: id, NodeID: node, Kind: "test", SHA256: digest, Size: size})
	return b
}

func TestEvidenceArtifactRehashAndPathSafety(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	data := "clean fixture bytes\n"
	path := writeArtifact(t, data)
	art, err := s.PutEvidenceFile(path, "", []string{"ev1", "n1", "test"})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(data))
	if art.SHA256 != hex.EncodeToString(want[:]) || art.Size != int64(len(data)) {
		t.Fatalf("unexpected artifact %+v", art)
	}
	entry := EvidenceEntry{EvidenceID: "ev1", NodeID: "n1", Kind: "test", SHA256: art.SHA256, Size: art.Size}
	if err := VerifyEvidenceArtifact(ws, entry); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(ws, "evidence", "blobs", art.SHA256)
	if err := os.WriteFile(blob, []byte("tampered same-ish length\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidenceArtifact(ws, entry); err == nil {
		t.Fatal("tampered artifact accepted")
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	outside := writeArtifact(t, data)
	if err := os.Symlink(outside, blob); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidenceArtifact(ws, entry); err == nil {
		t.Fatal("symlinked artifact accepted")
	}
	bad := entry
	bad.SHA256 = "../" + strings.Repeat("a", 61)
	if err := VerifyEvidenceArtifact(ws, bad); err == nil {
		t.Fatal("path-shaped digest accepted")
	}
}

func TestEvidencePutRejectsSecretsSymlinksAndStoreEscape(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	fakeAWS := "AKIAIOSFODNN7EXAMPLE"
	fakeGH := "ghp_1234567890abcdefghijklmnopqrstuvwx"
	cases := []struct {
		name string
		file string
		meta []string
	}{
		{"content", writeArtifact(t, "token="+fakeGH+"\n"), nil},
		{"metadata", writeArtifact(t, "clean\n"), []string{"kind", "password = hunter2hunter2"}},
	}
	fname := filepath.Join(t.TempDir(), fakeAWS+".txt")
	if err := os.WriteFile(fname, []byte("clean\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		name string
		file string
		meta []string
	}{"filename", fname, nil})
	for _, tc := range cases {
		_, err := s.PutEvidenceFile(tc.file, "", tc.meta)
		if err == nil {
			t.Fatalf("%s secret was accepted", tc.name)
		}
		if strings.Contains(err.Error(), fakeAWS) || strings.Contains(err.Error(), fakeGH) || strings.Contains(err.Error(), "hunter2hunter2") {
			t.Fatalf("%s error leaked secret: %v", tc.name, err)
		}
	}
	clean := writeArtifact(t, "clean\n")
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.Symlink(clean, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutEvidenceFile(link, "", nil); err == nil {
		t.Fatal("symlink source accepted")
	}
	outside := t.TempDir()
	ws2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws2, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws2, "evidence")); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(ws2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.PutEvidenceFile(clean, "", nil); err == nil {
		t.Fatal("symlinked evidence directory accepted")
	}
}

func TestDoneGateRequiresPersistedArtifactAndOwnership(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	_, created := seed(t, s)
	path := writeArtifact(t, "verified fixture\n")
	art, err := s.PutEvidenceFile(path, "", []string{"ev1", "n1", "test"})
	if err != nil {
		t.Fatal(err)
	}
	evBuf := evidencePayload("ev1", "n1", art.SHA256, art.Size)
	ee, _, err := s.Append(Event{Type: EvEvidenceAdded, CausalParents: []string{created.EventID}, Payload: evBuf})
	if err != nil {
		t.Fatalf("evidence append: %v", err)
	}
	ipPayload, _ := json.Marshal(NodePayload{NodeID: "n1", Status: string(StatusInProgress)})
	ip, _, err := s.Append(Event{Type: EvStatusChanged, CausalParents: []string{created.EventID}, BaseRevision: 1, ExpectedRevision: 2, LeaseID: "l1", FencingToken: 1, Payload: ipPayload})
	if err != nil {
		t.Fatalf("in_progress append: %v", err)
	}
	donePayload, _ := json.Marshal(NodePayload{NodeID: "n1", Status: string(StatusDone), EvidenceIDs: []string{"ev1"}})
	done, _, err := s.Append(Event{Type: EvStatusChanged, CausalParents: []string{ip.EventID, ee.EventID}, BaseRevision: 2, ExpectedRevision: 3, LeaseID: "l1", FencingToken: 1, Payload: donePayload})
	if err != nil {
		t.Fatalf("done with persisted artifact: %v", err)
	}
	_ = done
	r, err := s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	if r.Tree.Nodes["n1"].Status != StatusDone || len(r.Conflicts) != 0 {
		t.Fatalf("done gate failed: %+v", r)
	}

	// Evidence registered to n1 cannot satisfy done for n2.
	c2, _, err := s.Append(Event{Type: EvOwnershipClaimed, CausalParents: []string{}, LeaseID: "l2", FencingToken: 1, Payload: json.RawMessage(`{"node_id":"n2","owner":"a","lease_id":"l2","fencing_token":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	n2Payload, _ := json.Marshal(NodePayload{NodeID: "n2", Type: "task", Title: "n2", ParentID: "g1", Owner: "a"})
	if _, _, err := s.Append(Event{Type: EvNodeCreated, CausalParents: []string{c2.EventID}, LeaseID: "l2", FencingToken: 1, Payload: n2Payload}); err != nil {
		t.Fatal(err)
	}
	// Find constructed node's LastEventID indirectly by replay.
	r, _ = s.LoadReplay()
	n2 := r.Tree.Nodes["n2"]
	ip2Payload, _ := json.Marshal(NodePayload{NodeID: "n2", Status: string(StatusInProgress)})
	ip2, _, err := s.Append(Event{Type: EvStatusChanged, CausalParents: []string{n2.LastEventID}, BaseRevision: n2.Revision, ExpectedRevision: n2.Revision + 1, LeaseID: "l2", FencingToken: 1, Payload: ip2Payload})
	if err != nil {
		t.Fatal(err)
	}
	badDone, _ := json.Marshal(NodePayload{NodeID: "n2", Status: string(StatusDone), EvidenceIDs: []string{"ev1"}})
	if _, _, err := s.Append(Event{Type: EvStatusChanged, CausalParents: []string{ip2.EventID}, BaseRevision: 2, ExpectedRevision: 3, LeaseID: "l2", FencingToken: 1, Payload: badDone}); err == nil || !strings.Contains(err.Error(), "belongs to node") {
		t.Fatalf("cross-node evidence accepted: %v", err)
	}

	// Missing bytes invalidate the projection even for already-applied done.
	if err := os.Remove(filepath.Join(ws, "evidence", "blobs", art.SHA256)); err != nil {
		t.Fatal(err)
	}
	r, err = s.LoadReplay()
	if err != nil {
		t.Fatal(err)
	}
	if r.RecoveryStatus == "complete" {
		t.Fatalf("missing artifact still replayed complete: %+v", r)
	}
	found := false
	for _, c := range r.Conflicts {
		if strings.Contains(c.Reason, "missing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing artifact not surfaced: %+v", r.Conflicts)
	}
}

func TestEvidenceEventWithoutBytesRejected(t *testing.T) {
	ws := t.TempDir()
	s, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	_, created := seed(t, s)
	before, _ := s.LoadReport()
	fake := strings.Repeat("a", 64)
	if _, _, err := s.Append(Event{Type: EvEvidenceAdded, CausalParents: []string{created.EventID}, Payload: evidencePayload("ev1", "n1", fake, 4)}); err == nil {
		t.Fatal("evidence event without artifact bytes accepted")
	}
	after, _ := s.LoadReport()
	if len(after.Events) != len(before.Events) {
		t.Fatal("rejected evidence event wrote bytes")
	}
}
