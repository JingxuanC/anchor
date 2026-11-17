package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// fixturesSHA256 hashes the benchmark task files (relative path + content) so a
// report can be tied to an exact task revision (a task hash lock).
func fixturesSHA256(dir string) (string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gitCommitOf(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func anchorVersionOf(anchorBin string) string {
	if anchorBin == "" {
		return ""
	}
	out, err := exec.Command(anchorBin, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// budgetExceeded reports whether the accumulated agent usage exceeds the caps.
func budgetExceeded(trials []TrialResult, maxUSD float64, maxTokens int64) (bool, string) {
	if maxUSD <= 0 && maxTokens <= 0 {
		return false, ""
	}
	var cost float64
	var tokens int64
	for _, t := range trials {
		cost += t.Phase1Tokens.CostUSD + t.Phase2Tokens.CostUSD
		tokens += t.Phase1Tokens.Total() + t.Phase2Tokens.Total()
	}
	if maxUSD > 0 && cost > maxUSD {
		return true, fmt.Sprintf("total agent cost %.4f USD exceeded --max-usd %.4f after %d trial rows", cost, maxUSD, len(trials))
	}
	if maxTokens > 0 && tokens > maxTokens {
		return true, fmt.Sprintf("total agent tokens %d exceeded --max-tokens %d after %d trial rows", tokens, maxTokens, len(trials))
	}
	return false, ""
}

// writePatch stores the agent's patch for a phase (git diff against the last
// commit, before the harness commits the agent's work).
func writePatch(ctx context.Context, repo, path string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "diff", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	return writeFile(path, string(out))
}

// visibleTestFiles returns the canonical visible test files for the built-in
// resume task (files the agent can see and might try to edit).
func visibleTestFiles(fixturesDir string) map[string]string {
	// Project-relative path -> canonical fixture path.
	return map[string]string{
		"ratelimit_test.go": filepath.Join(fixturesDir, "ratelimit", "ratelimit_test.go"),
	}
}

// detectTestModification compares the project's visible test files with their
// canonical copies. Acceptance grading always reinstalls canonical tests, so a
// modified file can never turn a failing implementation into a pass.
func detectTestModification(fixturesDir, proj, phase string, baseline map[string]string) (bool, []string) {
	modified := false
	var flags []string
	for rel, canonical := range visibleTestFiles(fixturesDir) {
		current := filepath.Join(proj, rel)
		want, err := hashFileContent(canonical)
		if err != nil {
			continue
		}
		got, err := hashFileContent(current)
		if err != nil {
			flags = append(flags, phase+": visible test file missing or unreadable: "+rel)
			modified = true
			continue
		}
		if prev, ok := baseline[rel]; ok && prev != got {
			flags = append(flags, phase+": agent modified visible test file "+rel)
			modified = true
		} else if got != want {
			flags = append(flags, phase+": visible test file differs from the canonical copy "+rel)
			modified = true
		}
	}
	return modified, flags
}

func hashFileContent(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// taskVisibleHashes captures the visible test-file hashes right after the
// project copy, before the agent can touch them.
func taskVisibleHashes(fixturesDir, proj string) map[string]string {
	out := map[string]string{}
	for rel := range visibleTestFiles(fixturesDir) {
		if h, err := hashFileContent(filepath.Join(proj, rel)); err == nil {
			out[rel] = h
		}
	}
	return out
}

// cheatSuspicionFlags scans an agent log for obvious benchmark-answer search
// patterns (soft signal only; never used to change the score).
func cheatSuspicionFlags(logText string) []string {
	lower := strings.ToLower(logText)
	var flags []string
	for _, pat := range []string{"huggingface", "swe-bench", "polyglot-benchmark", "aider benchmark", "exercism solution"} {
		if strings.Contains(lower, pat) {
			flags = append(flags, "answer_search_suspected: "+pat)
		}
	}
	return flags
}
