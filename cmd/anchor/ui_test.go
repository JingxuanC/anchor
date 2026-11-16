package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIHandlerServesTreeAPIAndPage(t *testing.T) {
	root, proj := treeFixture(t)
	srv := httptest.NewServer(uiHandler(filepath.Join(proj, ".anchor")))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api status = %d", resp.StatusCode)
	}
	var view struct {
		WorkspaceID string `json:"workspace_id"`
		Nodes       []struct {
			ID       string `json:"id"`
			Decision string `json:"decision"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.WorkspaceID != "ws1" || len(view.Nodes) != 5 {
		t.Fatalf("api tree wrong: %+v", view)
	}
	foundDecision := false
	for _, n := range view.Nodes {
		if n.ID == "dec1" && n.Decision == "round to whole intervals" {
			foundDecision = true
		}
	}
	if !foundDecision {
		t.Fatalf("decision detail missing from API: %+v", view.Nodes)
	}

	page, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	body, _ := io.ReadAll(page.Body)
	text := string(body)
	if !strings.Contains(text, "Anchor Session Tree") || !strings.Contains(text, "/api/tree") {
		t.Fatalf("page is not the tree UI: %s", truncateText(text))
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatal("UI page must not reference external resources")
	}
	for _, forbidden := range []string{"innerHTML", "eval("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("UI must render data safely (found %q)", forbidden)
		}
	}
	if got := getStatus(t, srv.URL+"/nope"); got != http.StatusNotFound {
		t.Fatalf("/nope status = %d, want 404", got)
	}
	if got := getStatus(t, srv.URL+"/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz status = %d", got)
	}
	// Read-only: no mutation surface at all.
	for _, path := range []string{"/", "/api/tree"} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("POST %s Allow = %q", path, allow)
		}
	}
	_ = root
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func truncateText(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func TestUIHandlerReportsUnreadableWorkspace(t *testing.T) {
	srv := httptest.NewServer(uiHandler(filepath.Join(t.TempDir(), "missing")))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error == "" {
		t.Fatalf("error JSON missing: %v %v", body, err)
	}
}

func TestValidateUIAddr(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:0", "127.0.0.1:8080", "[::1]:0"} {
		if err := validateUIAddr(ok); err != nil {
			t.Fatalf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:0", ":8080", "192.168.1.10:8080", "example.com:80", "not-an-address"} {
		if err := validateUIAddr(bad); err == nil {
			t.Fatalf("%q accepted; the UI must be loopback-only", bad)
		}
	}
}
