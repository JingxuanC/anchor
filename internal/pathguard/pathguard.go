package pathguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var sharedAgentConfig = []string{
	".claude", ".codex", ".gemini",
	filepath.Join(".config", "claude"),
	filepath.Join(".config", "codex"),
	filepath.Join(".config", "gemini"),
}

// Canonical resolves symlinks in existing components and keeps the remaining
// non-existent suffix. It does not create anything.
func Canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	cur := abs
	var tail []string
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(abs), nil
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
}

func IsSharedAgentConfig(path string) (bool, error) {
	target, err := Canonical(path)
	if err != nil {
		return false, err
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false, nil
	}
	hc, err := Canonical(home)
	if err != nil {
		return false, err
	}
	for _, rel := range sharedAgentConfig {
		shared := filepath.Join(hc, rel)
		if target == shared || strings.HasPrefix(target, shared+string(os.PathSeparator)) {
			return true, nil
		}
	}
	return false, nil
}

func ValidateExistingDir(path string) (string, error) {
	canonical, err := Canonical(path)
	if err != nil {
		return "", err
	}
	if shared, err := IsSharedAgentConfig(canonical); err != nil {
		return "", err
	} else if shared {
		return "", fmt.Errorf("refusing shared agent config directory")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory")
	}
	return canonical, nil
}

func ValidateCreateDir(path string) (string, error) {
	canonical, err := Canonical(path)
	if err != nil {
		return "", err
	}
	if shared, err := IsSharedAgentConfig(canonical); err != nil {
		return "", err
	} else if shared {
		return "", fmt.Errorf("refusing shared agent config directory")
	}
	if info, err := os.Lstat(canonical); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("workspace path must not be a symlink")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("workspace path is not a directory")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return canonical, nil
}
