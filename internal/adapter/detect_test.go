package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBinary(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"2.1.252 (Claude Code)":          "2.1.252",
		"codex-cli 0.141.0":              "0.141.0",
		"v1.2.3":                         "1.2.3",
		"gemini 0.9.0-preview.1+build.5": "0.9.0-preview.1+build.5",
		"no version here":                "",
		"":                               "",
	}
	for in, want := range cases {
		if got := ParseVersion(in); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectUsesPathAndParsesVersion(t *testing.T) {
	pathDir := t.TempDir()
	fakeBinary(t, pathDir, "claude", "echo '2.9.7 (Claude Code)'\n")
	fakeBinary(t, pathDir, "codex", "echo 'codex-cli 1.4.0'\n")
	t.Setenv("PATH", pathDir)

	claude := Detect("claude-code", 5*time.Second)
	if !claude.Installed || claude.Version != "2.9.7" || claude.NativeHookStatus != "unverified" || claude.EndToEndVerified {
		t.Fatalf("claude detection wrong: %+v", claude)
	}
	codex := Detect("codex", 5*time.Second)
	if !codex.Installed || codex.Version != "1.4.0" {
		t.Fatalf("codex detection wrong: %+v", codex)
	}
	gemini := Detect("gemini-cli", 5*time.Second)
	if gemini.Installed || gemini.Error == "" {
		t.Fatalf("missing gemini should be reported as not installed: %+v", gemini)
	}
	if d := Detect("bogus", 5*time.Second); d.Installed || !strings.Contains(d.Error, "unknown adapter") {
		t.Fatalf("unknown adapter detection wrong: %+v", d)
	}
}

func TestDetectTimeoutAndFailure(t *testing.T) {
	pathDir := t.TempDir()
	fakeBinary(t, pathDir, "claude", "while :; do :; done\n")
	t.Setenv("PATH", pathDir)
	start := time.Now()
	d := Detect("claude-code", 120*time.Millisecond)
	if d.Installed || !strings.Contains(d.Error, "timed out") {
		t.Fatalf("hang was not reported as a timeout: %+v", d)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout not bounded: %v", elapsed)
	}
	// A non-zero exit with no output is an error, not "installed".
	pathDir2 := t.TempDir()
	fakeBinary(t, pathDir2, "codex", "exit 3\n")
	t.Setenv("PATH", pathDir2)
	d = Detect("codex", 5*time.Second)
	if d.Installed || d.Error == "" {
		t.Fatalf("failing binary reported as installed: %+v", d)
	}
}

func TestDetectAllIsDeterministic(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	all := DetectAll(200 * time.Millisecond)
	if len(all) != 3 {
		t.Fatalf("expected three adapters, got %d", len(all))
	}
	ids := []string{all[0].AdapterID, all[1].AdapterID, all[2].AdapterID}
	want := []string{"claude-code", "codex", "gemini-cli"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
}

// TestDetectRealClaudeWhenInstalled proves detection against the real CLI when it
// is present. It is the only real-CLI evidence this suite claims.
func TestDetectRealClaudeWhenInstalled(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skip("no env")
	}
	d := Detect("claude-code", 3*time.Second)
	if !d.Installed {
		t.Skipf("claude not installed in this environment: %s", d.Error)
	}
	if d.Version == "" || !strings.HasPrefix(d.Version, "2.") && !strings.Contains(d.Version, ".") {
		t.Fatalf("real claude version not parsed: %+v", d)
	}
	// Detection must never upgrade hook evidence.
	if d.NativeHookStatus != "unverified" || d.EndToEndVerified {
		t.Fatalf("detection overclaimed hook evidence: %+v", d)
	}
}
