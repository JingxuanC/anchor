package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JingxuanC/anchor/internal/adapter"
)

// skillState records one installed agent skill so uninstall only removes what
// Anchor wrote.
type skillState struct {
	SchemaVersion int    `json:"schema_version"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	InstalledAt   string `json:"installed_at"`
}

// skillPlan is the read-only plan (default) or applied result.
type skillPlan struct {
	Path             string   `json:"path"`
	BackupPath       string   `json:"backup_path,omitempty"`
	SHA256           string   `json:"sha256"`
	Applied          bool     `json:"applied"`
	AlreadyInstalled bool     `json:"already_installed,omitempty"`
	Uninstall        bool     `json:"uninstall,omitempty"`
	Removed          bool     `json:"removed,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

func cmdSkill(args []string) {
	if len(args) < 1 {
		skillUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "print":
		fmt.Print(adapter.SkillMarkdown)
	case "install":
		skillInstall(args[1:])
	default:
		skillUsage()
		os.Exit(2)
	}
}

func skillUsage() {
	fmt.Fprintln(os.Stderr, `  anchor skill print
  anchor skill install --dir DIR --repo PROJECT [--apply | --uninstall] [--json]`)
}

func skillInstall(args []string) {
	fs := flag.NewFlagSet("skill install", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	repo := fs.String("repo", "", "project directory that receives .claude/skills/anchor/SKILL.md")
	apply := fs.Bool("apply", false, "write the change (default is a plan)")
	uninstall := fs.Bool("uninstall", false, "remove the installed skill")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	store := openStore(*dir)
	statePath := filepath.Join(store.Dir, "adapters", "skill.json")
	skillSHA := sha256Hex(adapter.SkillMarkdown)
	plan := skillPlan{SHA256: skillSHA, Uninstall: *uninstall}

	if *uninstall {
		runSkillUninstall(&plan, statePath, *apply)
	} else {
		if *repo == "" {
			die(fmt.Errorf("--repo is required"))
		}
		repoDir, err := filepath.EvalSymlinks(*repo)
		if err != nil {
			die(fmt.Errorf("repo: %w", err))
		}
		if info, err := os.Stat(repoDir); err != nil || !info.IsDir() {
			die(fmt.Errorf("repo %s is not a directory", *repo))
		}
		plan.Path = filepath.Join(repoDir, ".claude", "skills", "anchor", "SKILL.md")
		runSkillInstall(&plan, statePath, *apply)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(b))
		return
	}
	verb := "install"
	if plan.Uninstall {
		verb = "uninstall"
	}
	fmt.Printf("%s anchor skill (path=%s applied=%v)\n", verb, plan.Path, plan.Applied)
	if plan.BackupPath != "" {
		fmt.Printf("  backup: %s\n", plan.BackupPath)
	}
	if plan.AlreadyInstalled {
		fmt.Println("  already installed; nothing to change")
	}
	if plan.Removed {
		fmt.Println("  removed the installed skill")
	}
	for _, w := range plan.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	if !plan.Applied && !plan.AlreadyInstalled && !plan.Uninstall {
		fmt.Println("  (plan only; re-run with --apply to write)")
	}
}

func runSkillInstall(plan *skillPlan, statePath string, apply bool) {
	existing, err := os.ReadFile(plan.Path)
	if err == nil {
		if sha256Hex(string(existing)) == plan.SHA256 {
			plan.AlreadyInstalled = true
			if st, err := loadSkillState(statePath); err == nil && st != nil {
				return
			}
			if !apply {
				return
			}
			if err := writeSkillState(statePath, plan.Path, plan.SHA256); err != nil {
				die(err)
			}
			plan.Applied = true
			return
		}
		backup := plan.Path + ".anchor-backup-" + time.Now().UTC().Format("20060102T150405.000000000")
		plan.BackupPath = backup
		if !apply {
			return
		}
		if err := copyFile0600(plan.Path, backup); err != nil {
			die(fmt.Errorf("cannot back up the existing skill: %w", err))
		}
	} else if !os.IsNotExist(err) {
		die(err)
	}
	if !apply {
		return
	}
	if err := secureWriteFile(plan.Path, adapter.SkillMarkdown); err != nil {
		die(err)
	}
	if err := writeSkillState(statePath, plan.Path, plan.SHA256); err != nil {
		die(err)
	}
	plan.Applied = true
}

func runSkillUninstall(plan *skillPlan, statePath string, apply bool) {
	st, err := loadSkillState(statePath)
	if err != nil {
		die(err)
	}
	if st == nil {
		plan.Warnings = append(plan.Warnings, "no recorded skill installation in this workspace")
		return
	}
	plan.Path = st.Path
	b, err := os.ReadFile(st.Path)
	if err != nil {
		if os.IsNotExist(err) {
			plan.Warnings = append(plan.Warnings, "installed skill file is missing; removing the recorded state only")
		} else {
			die(err)
		}
	} else if sha256Hex(string(b)) != st.SHA256 {
		plan.Warnings = append(plan.Warnings, "installed skill was edited; leaving it in place and removing the recorded state only")
	} else {
		if !apply {
			return
		}
		if err := os.Remove(st.Path); err != nil {
			die(err)
		}
		plan.Removed = true
		// Best-effort cleanup of now-empty skill directories.
		for _, d := range []string{filepath.Dir(st.Path), filepath.Dir(filepath.Dir(st.Path)), filepath.Dir(filepath.Dir(filepath.Dir(st.Path)))} {
			_ = os.Remove(d)
		}
	}
	if !apply {
		return
	}
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		die(err)
	}
	plan.Applied = true
}

func loadSkillState(path string) (*skillState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st skillState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("invalid skill state: %w", err)
	}
	return &st, nil
}

func writeSkillState(path, skillPath, sha string) error {
	st := skillState{SchemaVersion: 1, Path: skillPath, SHA256: sha, InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return secureWriteFile(path, string(b)+"\n")
}

// secureWriteFile writes a file atomically with 0600, creating directories
// without following symlinked parents.
func secureWriteFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to write into %s: not a real directory", dir)
	}
	tmp, err := os.CreateTemp(dir, ".skill-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func copyFile0600(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
