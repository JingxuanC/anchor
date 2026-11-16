//go:build !windows

package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCLIUIProcessServesAndStops starts the real binary, reads the advertised
// loopback URL, fetches the tree API, then stops it with SIGINT.
func TestCLIUIProcessServesAndStops(t *testing.T) {
	root, proj := treeFixture(t)
	cmd := exec.Command(anchorBin, "ui", "--dir", proj, "--addr", "127.0.0.1:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	urlCh := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "anchor ui listening on http://") {
				urlCh <- strings.TrimPrefix(line, "anchor ui listening on ")
				return
			}
		}
		urlCh <- ""
	}()
	var url string
	select {
	case url = <-urlCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("ui did not advertise a URL; stderr=%s", stderr.String())
	}
	if url == "" {
		t.Fatalf("ui did not print a URL; stderr=%s", stderr.String())
	}
	resp, err := http.Get(strings.TrimSuffix(url, "/") + "/api/tree")
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	var view struct {
		WorkspaceID string `json:"workspace_id"`
		Nodes       []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if view.WorkspaceID != "ws1" || len(view.Nodes) == 0 {
		t.Fatalf("ui API wrong: %+v", view)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ui exited after SIGINT: %v (stderr=%s)", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ui did not stop after SIGINT (stderr=%s)", stderr.String())
	}
	_ = filepath.Join(root, "unused")
}
