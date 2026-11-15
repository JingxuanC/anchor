package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RenderOutput struct {
	SchemaVersion              int        `json:"schema_version"`
	Adapter                    Capability `json:"adapter"`
	HookCommand                string     `json:"hook_command"`
	NativeConfigFile           string     `json:"native_config_file"`
	NativeConfig               string     `json:"native_config"`
	Installation               string     `json:"installation"`
	DoesNotOverwriteUserConfig bool       `json:"does_not_overwrite_user_config"`
	DoesNotRestoreOrBlock      bool       `json:"does_not_restore_or_block"`
}

type RenderedFile struct {
	Path    string `json:"path"`
	Content []byte `json:"-"`
}

func hookCommand(id string) string {
	return "anchor adapter capture --adapter " + id + " --stdin --print"
}

func nativeConfig(a Capability) (string, string, error) {
	cmd := hookCommand(a.ID)
	switch a.ID {
	case "claude-code":
		cfg := map[string]any{
			"_anchor_status": "UNVERIFIED documentation-only fragment; review and merge manually",
			"hooks": map[string]any{
				"PostToolUse": []any{map[string]any{
					"matcher": "*",
					"hooks":   []any{map[string]any{"type": "command", "command": cmd}},
				}},
			},
		}
		b, _ := json.MarshalIndent(cfg, "", "  ")
		return "settings.fragment.json", string(b) + "\n", nil
	case "gemini-cli":
		cfg := map[string]any{
			"_anchor_status": "UNVERIFIED documentation-only fragment; review and merge manually",
			"hooks": map[string]any{
				"PostToolUse": []any{map[string]any{"command": cmd}},
			},
		}
		b, _ := json.MarshalIndent(cfg, "", "  ")
		return "settings.fragment.json", string(b) + "\n", nil
	case "codex":
		cfg := "# ANCHOR STATUS: UNVERIFIED documentation-only fragment.\n" +
			"# Review before manual use. It does not install itself, restore, or block tool calls.\n" +
			"[hooks]\npost_tool_use = " + fmt.Sprintf("%q", cmd) + "\n"
		return "config.toml.fragment", cfg, nil
	default:
		return "", "", fmt.Errorf("unknown adapter %q", a.ID)
	}
}

func Render(id string) (RenderOutput, []RenderedFile, error) {
	a, err := Get(id)
	if err != nil {
		return RenderOutput{}, nil, err
	}
	nativeName, native, err := nativeConfig(a)
	if err != nil {
		return RenderOutput{}, nil, err
	}
	out := RenderOutput{
		SchemaVersion: 1, Adapter: a, HookCommand: hookCommand(a.ID),
		NativeConfigFile: nativeName, NativeConfig: native,
		Installation: "manual_review_only", DoesNotOverwriteUserConfig: true,
		DoesNotRestoreOrBlock: true,
	}
	manifest, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return RenderOutput{}, nil, err
	}
	files := []RenderedFile{
		{Path: "anchor-adapter.json", Content: append(manifest, '\n')},
		{Path: nativeName, Content: []byte(native)},
	}
	return out, files, nil
}

func isSharedConfigTarget(target string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false, err
	}
	target = filepath.Clean(target)
	for _, rel := range []string{".claude", ".codex", ".gemini", filepath.Join(".config", "claude"), filepath.Join(".config", "codex"), filepath.Join(".config", "gemini")} {
		shared := filepath.Clean(filepath.Join(home, rel))
		if target == shared || strings.HasPrefix(target, shared+string(os.PathSeparator)) {
			return true, nil
		}
	}
	return false, nil
}

// Write renders into an explicit target directory. It refuses known shared
// user-config directories and never overwrites an existing file.
func Write(id, target string) (RenderOutput, []string, error) {
	out, files, err := Render(id)
	if err != nil {
		return RenderOutput{}, nil, err
	}
	if strings.TrimSpace(target) == "" {
		return RenderOutput{}, nil, fmt.Errorf("--target is required for adapter render writes")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return RenderOutput{}, nil, err
	}
	resolved := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = r
	} else if parent, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		resolved = filepath.Join(parent, filepath.Base(abs))
	}
	if shared, err := isSharedConfigTarget(resolved); err != nil {
		return RenderOutput{}, nil, err
	} else if shared {
		return RenderOutput{}, nil, fmt.Errorf("refusing to render into a shared user CLI config directory")
	}
	dir := filepath.Join(abs, out.Adapter.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RenderOutput{}, nil, err
	}
	if info, err := os.Lstat(dir); err != nil {
		return RenderOutput{}, nil, err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return RenderOutput{}, nil, fmt.Errorf("adapter target must be a real directory, not a symlink")
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		p := filepath.Join(dir, f.Path)
		if _, err := os.Lstat(p); err == nil {
			return RenderOutput{}, nil, fmt.Errorf("refusing to overwrite existing file %s", p)
		} else if !os.IsNotExist(err) {
			return RenderOutput{}, nil, err
		}
		paths = append(paths, p)
	}
	for i, f := range files {
		p := paths[i]
		fh, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return RenderOutput{}, nil, err
		}
		if _, err := fh.Write(f.Content); err != nil {
			_ = fh.Close()
			return RenderOutput{}, nil, err
		}
		if err := fh.Close(); err != nil {
			return RenderOutput{}, nil, err
		}
	}
	return out, paths, nil
}

func hashRef(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
