package pathguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustMkdirAll creates path (and parents) and returns it.
func mustMkdirAll(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	return path
}

// mustSymlink creates link -> target, skipping on platforms without symlinks.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
}

// resolved returns the fully symlink-resolved form of an existing path. It is
// computed with the standard library, not with the function under test.
func resolved(t *testing.T, path string) string {
	t.Helper()
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolvedPath
}

// sharedAgentConfigPaths mirrors the shared locations the package must guard.
func sharedAgentConfigPaths() []string {
	return []string{
		".claude", ".codex", ".gemini",
		filepath.Join(".config", "claude"),
		filepath.Join(".config", "codex"),
		filepath.Join(".config", "gemini"),
	}
}

func TestCanonicalExistingDir(t *testing.T) {
	root := t.TempDir()
	dir := mustMkdirAll(t, filepath.Join(root, "existing", "nested"))

	got, err := Canonical(dir)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", dir, err)
	}
	if want := resolved(t, dir); got != want {
		t.Fatalf("Canonical(%q) = %q, want resolved path %q", dir, got, want)
	}
}

func TestCanonicalNonExistentSuffixPreserved(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "missing", "deeper", "file.txt")

	got, err := Canonical(target)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", target, err)
	}
	want := filepath.Join(resolved(t, root), "missing", "deeper", "file.txt")
	if got != want {
		t.Fatalf("Canonical(%q) = %q, want %q", target, got, want)
	}
	if _, err := os.Stat(got); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Canonical created or altered %q (stat err = %v); it must not create anything", got, err)
	}
}

func TestCanonicalResolvesSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	real := mustMkdirAll(t, filepath.Join(root, "real", "sub"))
	link := filepath.Join(root, "link")
	mustSymlink(t, filepath.Join(root, "real"), link)

	t.Run("existing symlink", func(t *testing.T) {
		got, err := Canonical(link)
		if err != nil {
			t.Fatalf("Canonical(%q): %v", link, err)
		}
		if want := resolved(t, filepath.Join(root, "real")); got != want {
			t.Fatalf("Canonical(%q) = %q, want %q", link, got, want)
		}
	})

	t.Run("symlink with non-existent suffix", func(t *testing.T) {
		target := filepath.Join(link, "sub", "new.txt")
		got, err := Canonical(target)
		if err != nil {
			t.Fatalf("Canonical(%q): %v", target, err)
		}
		want := filepath.Join(resolved(t, real), "new.txt")
		if got != want {
			t.Fatalf("Canonical(%q) = %q, want %q", target, got, want)
		}
	})

	t.Run("dangling symlink name is preserved as a suffix", func(t *testing.T) {
		dangling := filepath.Join(root, "dangling")
		mustSymlink(t, filepath.Join(root, "does-not-exist"), dangling)
		got, err := Canonical(filepath.Join(dangling, "child"))
		if err != nil {
			t.Fatalf("Canonical: %v", err)
		}
		want := filepath.Join(resolved(t, root), "dangling", "child")
		if got != want {
			t.Fatalf("Canonical(dangling child) = %q, want %q", got, want)
		}
	})
}

func TestCanonicalSymlinkLoopReturnsError(t *testing.T) {
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	mustSymlink(t, loop, loop) // self-referential symlink

	for _, path := range []string{loop, filepath.Join(loop, "child")} {
		got, err := Canonical(path)
		if err == nil {
			t.Fatalf("Canonical(%q) accepted a symlink loop: %q", path, got)
		}
		if got != "" {
			t.Fatalf("Canonical(%q) returned %q alongside error %v", path, got, err)
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Canonical(%q) symlink-loop error must not look like not-exist: %v", path, err)
		}
	}
}

func TestCanonicalRelativeInput(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	resolvedCwd := resolved(t, cwd)

	t.Run("nonexistent relative path", func(t *testing.T) {
		rel := filepath.Join("pathguard-relative", "does-not-exist", "leaf")
		got, err := Canonical(rel)
		if err != nil {
			t.Fatalf("Canonical(%q): %v", rel, err)
		}
		want := filepath.Join(resolvedCwd, "pathguard-relative", "does-not-exist", "leaf")
		if got != want {
			t.Fatalf("Canonical(%q) = %q, want %q", rel, got, want)
		}
	})

	t.Run("dot and empty input resolve to the working directory", func(t *testing.T) {
		for _, input := range []string{".", ""} {
			got, err := Canonical(input)
			if err != nil {
				t.Fatalf("Canonical(%q): %v", input, err)
			}
			if got != resolvedCwd {
				t.Fatalf("Canonical(%q) = %q, want %q", input, got, resolvedCwd)
			}
		}
	})

	t.Run("dot-dot is cleaned before resolving the suffix", func(t *testing.T) {
		rel := filepath.Join("pathguard-relative", "a", "..", "b")
		got, err := Canonical(rel)
		if err != nil {
			t.Fatalf("Canonical(%q): %v", rel, err)
		}
		want := filepath.Join(resolvedCwd, "pathguard-relative", "b")
		if got != want {
			t.Fatalf("Canonical(%q) = %q, want %q", rel, got, want)
		}
	})
}

// Canonical must reject a relative input when the process working directory is
// no longer usable. The test restores the working directory before it returns
// and never runs in parallel, so the chdir cannot leak to other tests.
func TestCanonicalUnusableWorkingDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir(%q): %v", tmp, err)
	}
	defer func() {
		// Restore permissions before t.TempDir cleanup and leave the process
		// back in its original directory.
		_ = os.Chmod(tmp, 0o700)
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("restore working directory %q: %v", oldWD, err)
		}
	}()
	if err := os.Chmod(tmp, 0o000); err != nil {
		t.Fatalf("Chmod(%q): %v", tmp, err)
	}

	got, err := Canonical("relative/path")
	if err == nil {
		t.Fatalf("Canonical accepted a relative path with an unusable working directory: %q", got)
	}
	if got != "" {
		t.Fatalf("Canonical returned %q alongside error %v", got, err)
	}
}

func TestCanonicalRoot(t *testing.T) {
	root := string(os.PathSeparator)
	got, err := Canonical(root)
	if err != nil {
		t.Fatalf("Canonical(%q): %v", root, err)
	}
	if got != filepath.Clean(root) {
		t.Fatalf("Canonical(%q) = %q", root, got)
	}
}

func TestIsSharedAgentConfigRealHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Materialize the shared directories so both the existing and the
	// non-existent branch of the comparison are exercised.
	for _, rel := range sharedAgentConfigPaths() {
		mustMkdirAll(t, filepath.Join(home, rel))
	}
	alias := filepath.Join(home, "alias-to-claude")
	mustSymlink(t, filepath.Join(home, ".claude"), alias)

	outside := t.TempDir()
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"shared dir", filepath.Join(home, ".claude"), true},
		{"shared dir trailing separator", home + string(os.PathSeparator) + ".claude" + string(os.PathSeparator), true},
		{"shared file under home", filepath.Join(home, ".codex", "config.toml"), true},
		{"shared .config dir", filepath.Join(home, ".config", "claude"), true},
		{"shared nested under .config", filepath.Join(home, ".config", "gemini", "settings.json"), true},
		{"nested deep under shared dir", filepath.Join(home, ".claude", "projects", "x", "settings.json"), true},
		{"symlink alias to shared dir", alias, true},
		{"home itself", home, false},
		{"lookalike .claudex", filepath.Join(home, ".claudex"), false},
		{"lookalike .claude-other", filepath.Join(home, ".claude-other"), false},
		{"lookalike .config/claudex", filepath.Join(home, ".config", "claudex"), false},
		{"lookalike .gemini.bak", filepath.Join(home, ".gemini.bak"), false},
		{"shared name outside home", filepath.Join(outside, ".claude"), false},
		{"sibling home-like prefix", home + "-other" + string(os.PathSeparator) + ".claude", false},
		{"parent escape from shared dir", filepath.Join(home, ".claude", "..", "scratch"), false},
		{"plain path under home", filepath.Join(home, "projects", "anchor"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsSharedAgentConfig(tc.path)
			if err != nil {
				t.Fatalf("IsSharedAgentConfig(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("IsSharedAgentConfig(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	t.Run("relative input", func(t *testing.T) {
		got, err := IsSharedAgentConfig(".")
		if err != nil {
			t.Fatalf("IsSharedAgentConfig(.) : %v", err)
		}
		if got {
			t.Fatal("the package working directory must not be classified as a shared agent config dir")
		}
	})
}

// HOME reached through a symlink (e.g. /var -> /private/var on macOS) must be
// canonicalized before comparison.
func TestIsSharedAgentConfigSymlinkedHome(t *testing.T) {
	root := t.TempDir()
	realHome := mustMkdirAll(t, filepath.Join(root, "real-home"))
	linkHome := filepath.Join(root, "link-home")
	mustSymlink(t, realHome, linkHome)
	t.Setenv("HOME", linkHome)

	for _, rel := range sharedAgentConfigPaths() {
		t.Run("shared/"+filepath.ToSlash(rel), func(t *testing.T) {
			for _, base := range []string{linkHome, realHome} {
				got, err := IsSharedAgentConfig(filepath.Join(base, rel, "nested", "file.json"))
				if err != nil {
					t.Fatalf("IsSharedAgentConfig: %v", err)
				}
				if !got {
					t.Fatalf("shared path %q under HOME %q not recognized", filepath.Join(base, rel), base)
				}
			}
		})
	}

	if got, err := IsSharedAgentConfig(filepath.Join(linkHome, ".claudex")); err != nil {
		t.Fatalf("IsSharedAgentConfig lookalike: %v", err)
	} else if got {
		t.Fatal("lookalike .claudex under symlinked HOME classified as shared")
	}
}

func TestIsSharedAgentConfigMissingHome(t *testing.T) {
	t.Run("empty HOME", func(t *testing.T) {
		t.Setenv("HOME", "")
		got, err := IsSharedAgentConfig(filepath.Join(t.TempDir(), ".claude"))
		if err != nil {
			t.Fatalf("IsSharedAgentConfig: %v", err)
		}
		if got {
			t.Fatal("shared check succeeded without a resolvable HOME")
		}
	})

	t.Run("nonexistent HOME keeps its canonical suffix", func(t *testing.T) {
		missingHome := filepath.Join(t.TempDir(), "missing-home")
		t.Setenv("HOME", missingHome)
		got, err := IsSharedAgentConfig(filepath.Join(missingHome, ".claude"))
		if err != nil {
			t.Fatalf("IsSharedAgentConfig: %v", err)
		}
		if !got {
			t.Fatalf("shared path under non-existent HOME %q not recognized", missingHome)
		}
		if got, _ := IsSharedAgentConfig(t.TempDir()); got {
			t.Fatal("unrelated temp dir classified as shared under non-existent HOME")
		}
	})
}

func TestIsSharedAgentConfigErrorPaths(t *testing.T) {
	t.Run("unresolvable path", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		loop := filepath.Join(home, "loop")
		mustSymlink(t, loop, loop)
		got, err := IsSharedAgentConfig(filepath.Join(loop, "child"))
		if err == nil {
			t.Fatalf("IsSharedAgentConfig accepted a symlink-loop path (result %v)", got)
		}
		if got {
			t.Fatal("error result must report not-shared")
		}
	})

	t.Run("unresolvable HOME", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), "home-loop")
		mustSymlink(t, loop, loop)
		t.Setenv("HOME", loop)
		got, err := IsSharedAgentConfig(t.TempDir())
		if err == nil {
			t.Fatalf("IsSharedAgentConfig ignored an unresolvable HOME (result %v)", got)
		}
		if got {
			t.Fatal("error result must report not-shared")
		}
	})

	t.Run("unreadable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root bypasses directory permissions")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		locked := filepath.Join(t.TempDir(), "locked")
		mustMkdirAll(t, filepath.Join(locked, "child"))
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		got, err := IsSharedAgentConfig(filepath.Join(locked, "child"))
		if err == nil {
			t.Fatalf("IsSharedAgentConfig ignored an unreadable parent (result %v)", got)
		}
		if got {
			t.Fatal("error result must report not-shared")
		}
	})
}

func TestValidateExistingDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("existing directory", func(t *testing.T) {
		dir := mustMkdirAll(t, filepath.Join(t.TempDir(), "workspace"))
		got, err := ValidateExistingDir(dir)
		if err != nil {
			t.Fatalf("ValidateExistingDir(%q): %v", dir, err)
		}
		if want := resolved(t, dir); got != want {
			t.Fatalf("got %q, want canonical %q", got, want)
		}
	})

	t.Run("home itself is allowed", func(t *testing.T) {
		got, err := ValidateExistingDir(home)
		if err != nil {
			t.Fatalf("ValidateExistingDir(home): %v", err)
		}
		if got != resolved(t, home) {
			t.Fatalf("got %q, want %q", got, resolved(t, home))
		}
	})

	t.Run("non-existent directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing")
		if got, err := ValidateExistingDir(dir); err == nil {
			t.Fatalf("ValidateExistingDir(%q) accepted a missing dir: %q", dir, got)
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing dir error does not wrap fs.ErrNotExist: %v", err)
		}
	})

	t.Run("regular file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file.txt")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateExistingDir(file); err == nil {
			t.Fatal("ValidateExistingDir accepted a regular file")
		} else if !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("symlink to directory resolves to target", func(t *testing.T) {
		real := mustMkdirAll(t, filepath.Join(t.TempDir(), "real"))
		link := filepath.Join(t.TempDir(), "link")
		mustSymlink(t, real, link)
		got, err := ValidateExistingDir(link)
		if err != nil {
			t.Fatalf("ValidateExistingDir(%q): %v", link, err)
		}
		if want := resolved(t, real); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("symlink loop", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), "loop")
		mustSymlink(t, loop, loop)
		if got, err := ValidateExistingDir(loop); err == nil {
			t.Fatalf("ValidateExistingDir accepted a loop: %q", got)
		}
	})

	for _, rel := range sharedAgentConfigPaths() {
		t.Run("shared/"+filepath.ToSlash(rel), func(t *testing.T) {
			created := mustMkdirAll(t, filepath.Join(home, rel))
			if _, err := ValidateExistingDir(created); err == nil {
				t.Fatalf("ValidateExistingDir accepted shared config %q", created)
			} else if !strings.Contains(err.Error(), "shared agent config") {
				t.Fatalf("unexpected shared-config error: %v", err)
			}
		})
	}

	t.Run("relative path", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		got, err := ValidateExistingDir(".")
		if err != nil {
			t.Fatalf("ValidateExistingDir(.): %v", err)
		}
		if want := resolved(t, cwd); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestValidateCreateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("non-existent directory is accepted", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "new", "workspace")
		got, err := ValidateCreateDir(target)
		if err != nil {
			t.Fatalf("ValidateCreateDir(%q): %v", target, err)
		}
		want := filepath.Join(resolved(t, root), "new", "workspace")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		if _, err := os.Stat(got); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ValidateCreateDir created %q", got)
		}
	})

	t.Run("existing directory", func(t *testing.T) {
		dir := mustMkdirAll(t, filepath.Join(t.TempDir(), "existing"))
		got, err := ValidateCreateDir(dir)
		if err != nil {
			t.Fatalf("ValidateCreateDir(%q): %v", dir, err)
		}
		if want := resolved(t, dir); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("home itself is allowed", func(t *testing.T) {
		got, err := ValidateCreateDir(home)
		if err != nil {
			t.Fatalf("ValidateCreateDir(home): %v", err)
		}
		if got != resolved(t, home) {
			t.Fatalf("got %q, want %q", got, resolved(t, home))
		}
	})

	t.Run("regular file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file.txt")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateCreateDir(file); err == nil {
			t.Fatal("ValidateCreateDir accepted a regular file")
		} else if !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("dangling symlink is rejected", func(t *testing.T) {
		root := t.TempDir()
		link := filepath.Join(root, "dangling")
		mustSymlink(t, filepath.Join(root, "missing-target"), link)
		if _, err := ValidateCreateDir(link); err == nil {
			t.Fatal("ValidateCreateDir accepted a dangling symlink")
		} else if !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("symlink loop", func(t *testing.T) {
		loop := filepath.Join(t.TempDir(), "loop")
		mustSymlink(t, loop, loop)
		if got, err := ValidateCreateDir(loop); err == nil {
			t.Fatalf("ValidateCreateDir accepted a loop: %q", got)
		}
	})

	t.Run("existing symlink to a directory is rejected", func(t *testing.T) {
		real := mustMkdirAll(t, filepath.Join(t.TempDir(), "real"))
		link := filepath.Join(t.TempDir(), "link")
		mustSymlink(t, real, link)
		// A symlinked target is refused before canonicalization resolves it away
		// (docs/04 T-02); parents may still be symlinked.
		if got, err := ValidateCreateDir(link); err == nil {
			t.Fatalf("ValidateCreateDir accepted a symlinked target: %q", got)
		} else if !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("non-existent child below a symlinked parent", func(t *testing.T) {
		real := mustMkdirAll(t, filepath.Join(t.TempDir(), "real"))
		link := filepath.Join(t.TempDir(), "link")
		mustSymlink(t, real, link)
		got, err := ValidateCreateDir(filepath.Join(link, "new"))
		if err != nil {
			t.Fatalf("ValidateCreateDir: %v", err)
		}
		want := filepath.Join(resolved(t, real), "new")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("symlink to shared config is rejected", func(t *testing.T) {
		shared := mustMkdirAll(t, filepath.Join(home, ".claude"))
		link := filepath.Join(t.TempDir(), "alias")
		mustSymlink(t, shared, link)
		// The symlinked target is refused before the shared-config check, which is
		// at least as strict; both rejections are acceptable here.
		if _, err := ValidateCreateDir(link); err == nil {
			t.Fatal("ValidateCreateDir accepted a symlink into shared config")
		} else if !strings.Contains(err.Error(), "shared agent config") && !strings.Contains(err.Error(), "must not be a symlink") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	for _, rel := range sharedAgentConfigPaths() {
		t.Run("shared/"+filepath.ToSlash(rel), func(t *testing.T) {
			target := filepath.Join(home, rel, "workspace")
			if _, err := ValidateCreateDir(target); err == nil {
				t.Fatalf("ValidateCreateDir accepted shared config %q", target)
			} else if !strings.Contains(err.Error(), "shared agent config") {
				t.Fatalf("unexpected shared-config error: %v", err)
			}
		})
	}

	t.Run("relative path", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		rel := filepath.Join("pathguard-create-relative", "leaf")
		got, err := ValidateCreateDir(rel)
		if err != nil {
			t.Fatalf("ValidateCreateDir(%q): %v", rel, err)
		}
		want := filepath.Join(resolved(t, cwd), "pathguard-create-relative", "leaf")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// Both validators must surface a HOME that cannot be canonicalized instead of
// silently treating the path as safe.
func TestValidateDirUnresolvableHome(t *testing.T) {
	loop := filepath.Join(t.TempDir(), "home-loop")
	mustSymlink(t, loop, loop)
	t.Setenv("HOME", loop)
	safe := t.TempDir()

	if got, err := ValidateExistingDir(safe); err == nil {
		t.Fatalf("ValidateExistingDir ignored unresolvable HOME: %q", got)
	}
	if got, err := ValidateCreateDir(safe); err == nil {
		t.Fatalf("ValidateCreateDir ignored unresolvable HOME: %q", got)
	}
}

func TestIsSharedAgentConfigThroughSymlinkedConfigDir(t *testing.T) {
	home := t.TempDir()
	real := mustMkdirAll(t, filepath.Join(home, "dotfiles", "claude"))
	mustSymlink(t, real, filepath.Join(home, ".claude"))
	t.Setenv("HOME", home)
	inside := filepath.Join(real, "settings.json")
	if shared, err := IsSharedAgentConfig(inside); err != nil {
		t.Fatal(err)
	} else if !shared {
		t.Fatalf("path inside a symlinked ~/.claude was not classified as shared: %s", inside)
	}
	// A lookalike sibling remains allowed.
	if shared, err := IsSharedAgentConfig(filepath.Join(home, "dotfiles", "claude-notes")); err != nil {
		t.Fatal(err)
	} else if shared {
		t.Fatal("lookalike path under the real config parent classified as shared")
	}
	if _, err := ValidateCreateDir(filepath.Join(real, "workspace")); err == nil {
		t.Fatal("ValidateCreateDir accepted a workspace inside a symlinked shared config dir")
	}
}

func TestPathThroughRegularFileIsRejected(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ENOTDIR from lstat/stat must surface as an error, not be treated as "not
	// exists" (which would let a create proceed through a non-directory).
	if got, err := ValidateCreateDir(filepath.Join(file, "child")); err == nil {
		t.Fatalf("ValidateCreateDir accepted a path through a regular file: %q", got)
	}
	if got, err := ValidateExistingDir(filepath.Join(file, "child")); err == nil {
		t.Fatalf("ValidateExistingDir accepted a path through a regular file: %q", got)
	}
	if got, err := Canonical(filepath.Join(file, "child")); err == nil {
		t.Fatalf("Canonical accepted a path through a regular file: %q", got)
	}
}
