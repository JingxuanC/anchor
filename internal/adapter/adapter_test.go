package adapter

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "update golden files")

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Fatalf("golden mismatch for %s\nwant:\n%s\ngot:\n%s", path, want, got)
	}
}

func TestRenderGolden(t *testing.T) {
	for _, a := range All() {
		out, files, err := Render(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		filePayload := make([]map[string]any, 0, len(files))
		for _, f := range files {
			filePayload = append(filePayload, map[string]any{"path": f.Path, "content": string(f.Content)})
		}
		payload := map[string]any{"output": out, "files": filePayload}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, '\n')
		checkGolden(t, filepath.Join("testdata", "golden", a.ID+".json"), b)
	}
}

func TestCaptureGoldenAndContentOmission(t *testing.T) {
	for _, a := range All() {
		input, err := os.ReadFile(filepath.Join("testdata", "capture", a.ID+".input.json"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Capture(a.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, '\n')
		if strings.Contains(string(b), "must-not-be-copied") || strings.Contains(string(b), "echo fixture") {
			t.Fatalf("%s leaked content fields: %s", a.ID, b)
		}
		if got.NativeHookVerified || !got.DoesNotRestoreOrBlock {
			t.Fatalf("%s overclaimed native behavior: %+v", a.ID, got)
		}
		checkGolden(t, filepath.Join("testdata", "golden", "capture-"+a.ID+".json"), b)
	}
}

func TestCaptureRejectsSecrets(t *testing.T) {
	input := []byte(`{"hook_event_name":"PostToolUse","session_id":"s","tool_name":"Bash","prompt":"AKIAIOSFODNN7EXAMPLE"}`)
	if _, err := Capture("claude-code", input); err == nil {
		t.Fatal("capture accepted a secret-bearing input")
	}
}

func TestWriteRefusesSharedConfigAndOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, _, err := Write("claude-code", filepath.Join(home, ".claude")); err == nil {
		t.Fatal("write to shared ~/.claude target accepted")
	}
	link := filepath.Join(t.TempDir(), "looks-temp")
	if err := os.Symlink(filepath.Join(home, ".claude"), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Write("claude-code", link); err == nil {
		t.Fatal("symlink to shared ~/.claude accepted")
	}
	target := t.TempDir()
	if _, paths, err := Write("claude-code", target); err != nil {
		t.Fatal(err)
	} else if len(paths) != 2 {
		t.Fatalf("unexpected rendered files: %v", paths)
	}
	if _, _, err := Write("claude-code", target); err == nil {
		t.Fatal("second render overwrote existing files")
	}
}

func TestCaptureSourceIdentityAndIdempotency(t *testing.T) {
	base := `{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/tmp/c","tool_name":"Bash","source_event_id":"%s","source_seq":7}`
	a, err := Capture("claude-code", []byte(strings.Replace(base, "%s", "delivery-a", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.SourceEventRef, "sha256:") || a.SourceSeq != 7 {
		t.Fatalf("source identity missing: %+v", a)
	}
	if strings.Contains(a.SourceEventRef, "delivery-a") {
		t.Fatal("raw source event id leaked into hash field")
	}
	b, err := Capture("claude-code", []byte(strings.Replace(base, "%s", "delivery-b", 1)))
	if err != nil {
		t.Fatal(err)
	}
	ka, err := IdempotencyKey("claude-code", "task1", "fixture", a)
	if err != nil {
		t.Fatal(err)
	}
	ka2, _ := IdempotencyKey("claude-code", "task1", "fixture", a)
	kb, _ := IdempotencyKey("claude-code", "task1", "fixture", b)
	if ka != ka2 {
		t.Fatal("same source identity produced different idempotency keys")
	}
	if ka == kb {
		t.Fatal("different source identities produced the same idempotency key")
	}
	a2 := a
	a2.SourceSeq = 8
	a2.Tool = "Read"
	ka3, _ := IdempotencyKey("claude-code", "task1", "fixture", a2)
	if ka3 != ka {
		t.Fatal("same source identity with changed metadata/seq produced a different key")
	}
}
