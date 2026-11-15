package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Hook installation is deliberately narrow: only Claude Code's settings.json is
// managed in this build, because that is the adapter whose hook schema has been
// reviewed here. Other adapters only report their version.
const (
	ScopeProject = "project"
	ScopeUser    = "user"

	installSchemaVersion = 1
)

// HookSpec is one managed hook entry.
type HookSpec struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
	Timeout int    `json:"timeout"`
	Command string `json:"command"`
}

// InstallState is the on-disk record of what Anchor installed, used for
// drift-free uninstall and for doctor reporting.
type InstallState struct {
	SchemaVersion  int        `json:"schema_version"`
	AdapterID      string     `json:"adapter_id"`
	Scope          string     `json:"scope"`
	SettingsPath   string     `json:"settings_path"`
	AnchorPath     string     `json:"anchor_path"`
	Entries        []HookSpec `json:"entries"`
	AppliedAt      string     `json:"applied_at"`
	SettingsSHA256 string     `json:"settings_sha256"`
	Warnings       []string   `json:"warnings,omitempty"`
}

// InstallOptions describes one install/uninstall request.
type InstallOptions struct {
	AdapterID  string
	StateDir   string
	RepoDir    string
	Scope      string
	AnchorPath string
	Apply      bool
	Uninstall  bool
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// InstallPlan is the read-only plan (default) or the applied result.
type InstallPlan struct {
	SchemaVersion    int        `json:"schema_version"`
	AdapterID        string     `json:"adapter_id"`
	Scope            string     `json:"scope"`
	SettingsPath     string     `json:"settings_path"`
	StatePath        string     `json:"state_path"`
	AnchorPath       string     `json:"anchor_path"`
	Desired          []HookSpec `json:"desired,omitempty"`
	Operations       []string   `json:"operations,omitempty"`
	BackupPath       string     `json:"backup_path,omitempty"`
	Applied          bool       `json:"applied"`
	Uninstall        bool       `json:"uninstall,omitempty"`
	AlreadyInstalled bool       `json:"already_installed,omitempty"`
	Removed          int        `json:"removed,omitempty"`
	Warnings         []string   `json:"warnings,omitempty"`
}

// Install plans (and optionally applies) Claude Code hook installation.
func Install(opts InstallOptions) (InstallPlan, error) {
	if opts.AdapterID != "claude-code" {
		return InstallPlan{}, fmt.Errorf("hook installation is implemented for claude-code only in this build")
	}
	switch opts.Scope {
	case "", ScopeProject:
		opts.Scope = ScopeProject
	case ScopeUser:
	default:
		return InstallPlan{}, fmt.Errorf("invalid --scope %q (project|user)", opts.Scope)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	stateDir, err := existingDir(opts.StateDir)
	if err != nil {
		return InstallPlan{}, fmt.Errorf("state dir: %w", err)
	}
	anchorPath, err := resolveAnchorPath(opts.AnchorPath)
	if err != nil {
		return InstallPlan{}, err
	}
	settingsPath, err := settingsPath(opts.Scope, opts.RepoDir)
	if err != nil {
		return InstallPlan{}, err
	}
	statePath := filepath.Join(stateDir, "adapters", "claude-code.json")
	plan := InstallPlan{SchemaVersion: installSchemaVersion, AdapterID: "claude-code", Scope: opts.Scope,
		SettingsPath: settingsPath, StatePath: statePath, AnchorPath: anchorPath, Uninstall: opts.Uninstall}

	if opts.Uninstall {
		return uninstallHooks(plan, statePath, settingsPath, opts)
	}

	captures := filepath.Join(stateDir, "local", "captures")
	desired := managedHooks(anchorPath, captures)
	plan.Desired = desired
	settings, exists, err := loadSettings(settingsPath)
	if err != nil {
		return InstallPlan{}, err
	}
	if !exists {
		settings = map[string]any{}
	}
	changed, operations, err := mergeHooks(settings, desired)
	if err != nil {
		return InstallPlan{}, err
	}
	plan.Operations = operations
	if !changed {
		plan.AlreadyInstalled = true
		return plan, nil
	}
	plan.BackupPath = backupPath(settingsPath, exists, opts.Now())
	if !opts.Apply {
		return plan, nil
	}
	if err := writeSettings(settingsPath, settings, exists, opts.Now()); err != nil {
		return InstallPlan{}, err
	}
	plan.Applied = true
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return InstallPlan{}, err
	}
	sum := sha256.Sum256(append(b, '\n'))
	state := InstallState{SchemaVersion: installSchemaVersion, AdapterID: "claude-code", Scope: opts.Scope,
		SettingsPath: settingsPath, AnchorPath: anchorPath, Entries: desired,
		AppliedAt: opts.Now().UTC().Format(time.RFC3339), SettingsSHA256: hex.EncodeToString(sum[:])}
	if err := writeState(statePath, state); err != nil {
		return InstallPlan{}, err
	}
	return plan, nil
}

// managedHooks returns the hook entries Anchor installs. They only record
// metadata: the capture command writes a timestamped JSON file into the
// workspace and prints nothing, so hook output never pollutes agent context.
func managedHooks(anchorPath, capturesDir string) []HookSpec {
	command := shellQuote(anchorPath) + " adapter capture --adapter claude-code --stdin --capture-dir " + shellQuote(capturesDir)
	return []HookSpec{
		{Event: "PostToolUse", Matcher: "*", Timeout: 5, Command: command},
		{Event: "SessionEnd", Matcher: "*", Timeout: 1, Command: command},
	}
}

// mergeHooks inserts missing managed entries and reports what it would change.
func mergeHooks(settings map[string]any, desired []HookSpec) (bool, []string, error) {
	hooksAny, ok := settings["hooks"]
	if !ok {
		hooksAny = map[string]any{}
		settings["hooks"] = hooksAny
	}
	hooks, ok := hooksAny.(map[string]any)
	if !ok {
		return false, nil, fmt.Errorf("refusing to modify settings: hooks value is not an object")
	}
	var ops []string
	for _, spec := range desired {
		groups := eventGroups(hooks, spec.Event)
		if groupHasHook(groups, spec) {
			continue
		}
		hook := map[string]any{"type": "command", "command": spec.Command, "timeout": spec.Timeout}
		added := false
		for i := range groups {
			g, ok := groups[i].(map[string]any)
			if !ok || g["matcher"] != spec.Matcher {
				continue
			}
			existing, _ := g["hooks"].([]any)
			g["hooks"] = append(existing, hook)
			added = true
			ops = append(ops, "add "+spec.Event+" hook to existing matcher "+spec.Matcher)
			break
		}
		if !added {
			// No matching group: create one.
			group := map[string]any{"matcher": spec.Matcher, "hooks": []any{hook}}
			hooks[spec.Event] = append(groups, group)
			ops = append(ops, "add "+spec.Event+" hook in a new matcher group")
		}
	}
	sort.Strings(ops)
	return len(ops) > 0, ops, nil
}

// eventGroups returns the raw groups for an event, creating the slice if needed.
func eventGroups(hooks map[string]any, event string) []any {
	if v, ok := hooks[event]; ok {
		if groups, ok := v.([]any); ok {
			return groups
		}
		// Malformed value: replace it rather than crash.
		groups := []any{}
		hooks[event] = groups
		return groups
	}
	groups := []any{}
	hooks[event] = groups
	return groups
}

func groupHasHook(groups []any, spec HookSpec) bool {
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		list, _ := group["hooks"].([]any)
		for _, h := range list {
			hook, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if hook["type"] == "command" && hook["command"] == spec.Command {
				if timeout, ok := hook["timeout"].(float64); !ok || int(timeout) != spec.Timeout {
					continue
				}
				return true
			}
		}
	}
	return false
}

func uninstallHooks(plan InstallPlan, statePath, settingsPath string, opts InstallOptions) (InstallPlan, error) {
	state, err := LoadInstallState(opts.StateDir, "claude-code")
	if err != nil {
		return InstallPlan{}, err
	}
	if state == nil {
		plan.Warnings = append(plan.Warnings, "no recorded installation for claude-code in this workspace")
		return plan, nil
	}
	settings, exists, err := loadSettings(settingsPath)
	if err != nil {
		return InstallPlan{}, err
	}
	if !exists {
		plan.Warnings = append(plan.Warnings, "settings file is missing; removing the recorded installation only")
		if opts.Apply {
			if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
				return InstallPlan{}, err
			}
			plan.Applied = true
		}
		return plan, nil
	}
	removed := 0
	for _, spec := range state.Entries {
		removed += removeHook(settings, spec)
	}
	plan.Removed = removed
	plan.Operations = []string{fmt.Sprintf("remove %d managed hook entr(ies)", removed)}
	if removed == 0 {
		plan.Warnings = append(plan.Warnings, "no matching managed entries found (already uninstalled or edited); removing the recorded state only")
	}
	if !opts.Apply {
		return plan, nil
	}
	plan.BackupPath = backupPath(settingsPath, true, opts.Now())
	if !opts.Apply {
		return plan, nil
	}
	if err := writeSettings(settingsPath, settings, true, opts.Now()); err != nil {
		return InstallPlan{}, err
	}
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return InstallPlan{}, err
	}
	plan.Applied = true
	return plan, nil
}

func removeHook(settings map[string]any, spec HookSpec) int {
	hooksAny, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	groups, ok := hooksAny[spec.Event].([]any)
	if !ok {
		return 0
	}
	removed := 0
	keptGroups := make([]any, 0, len(groups))
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			keptGroups = append(keptGroups, g)
			continue
		}
		list, ok := group["hooks"].([]any)
		if !ok {
			keptGroups = append(keptGroups, g)
			continue
		}
		keptHooks := make([]any, 0, len(list))
		for _, h := range list {
			hook, ok := h.(map[string]any)
			if ok && hookMatches(hook, spec) {
				removed++
				continue
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 && len(list) > 0 {
			// Drop an empty group we emptied completely.
			continue
		}
		group["hooks"] = keptHooks
		keptGroups = append(keptGroups, group)
	}
	hooksAny[spec.Event] = keptGroups
	return removed
}

func hookMatches(hook map[string]any, spec HookSpec) bool {
	if hook["type"] != "command" || hook["command"] != spec.Command {
		return false
	}
	timeout, _ := hook["timeout"].(float64)
	return int(timeout) == spec.Timeout
}

// LoadInstallState reads the recorded installation for an adapter.
func LoadInstallState(stateDir, adapterID string) (*InstallState, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "adapters", adapterID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st InstallState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("invalid adapter install state: %w", err)
	}
	return &st, nil
}

func loadSettings(path string) (map[string]any, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, false, nil
		}
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("refusing to modify %s: not a regular file", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	var settings map[string]any
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, false, fmt.Errorf("refusing to modify %s: not a JSON object (%v)", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, true, nil
}

func writeSettings(path string, settings map[string]any, exists bool, now time.Time) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to write into %s: not a real directory", dir)
	}
	if exists {
		if err := copyFile(path, backupPath(path, true, now)); err != nil {
			return fmt.Errorf("cannot back up settings: %w", err)
		}
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func writeState(path string, st InstallState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func settingsPath(scope, repoDir string) (string, error) {
	switch scope {
	case ScopeProject:
		if repoDir == "" {
			return "", fmt.Errorf("--repo is required for project scope")
		}
		repo, err := existingDir(repoDir)
		if err != nil {
			return "", fmt.Errorf("repo: %w", err)
		}
		dir := filepath.Join(repo, ".claude")
		if info, err := os.Lstat(dir); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("refusing to install into %s: not a real directory", dir)
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return filepath.Join(dir, "settings.json"), nil
	case ScopeUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(home, ".claude")
		if info, err := os.Lstat(dir); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("refusing to install into %s: not a real directory", dir)
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return filepath.Join(dir, "settings.json"), nil
	}
	return "", fmt.Errorf("invalid scope")
}

func existingDir(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return real, nil
}

func resolveAnchorPath(given string) (string, error) {
	path := given
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("cannot locate the anchor executable: %w", err)
		}
		path = exe
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("anchor executable %s is not usable: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil || info.Mode()&0o111 == 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("anchor executable %s is not an executable regular file", real)
	}
	return real, nil
}

func backupPath(settingsPath string, exists bool, now time.Time) string {
	if !exists {
		return ""
	}
	return settingsPath + ".anchor-backup-" + now.UTC().Format("20060102T150405.000000000")
}

// shellQuote single-quotes a string for a POSIX shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// equalHook is used by tests.
func equalHook(a, b map[string]any) bool { return reflect.DeepEqual(a, b) }
