package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// conventionsFile is the on-disk fixture convention declaration
// (bench/fixtures/stress/<task>/conventions.json).
type conventionsFile struct {
	SchemaVersion   int              `json:"schema_version"`
	Task            string           `json:"task"`
	Package         string           `json:"package"`
	ImportPath      string           `json:"import_path"`
	Phase1File      string           `json:"phase1_file"`
	Phase2File      string           `json:"phase2_file"`
	VisibleTests    []string         `json:"visible_tests"`
	MockPhase1      string           `json:"mock_phase1"`
	MockPhase2Wrong string           `json:"mock_phase2_wrong"`
	DecisionTitle   string           `json:"decision_title"`
	Variants        []fixtureVariant `json:"variants"`
}

// fixtureVariant is one randomized hidden convention.
type fixtureVariant struct {
	ID         string `json:"id"`
	Statement  string `json:"statement"`
	Phrase     string `json:"phrase"`
	HiddenTest string `json:"hidden_test"`
	MockPhase2 string `json:"mock_phase2"`
}

// LoadedVariant is a fixture variant with its file contents resolved.
type LoadedVariant struct {
	ID             string
	Statement      string
	Phrase         string
	HiddenTestPath string
	HiddenTest     string
	MockPhase2Path string
	MockPhase2     string
}

// Fixture is one authored stress task, fully loaded into memory.
type Fixture struct {
	Name            string
	Dir             string
	RepoDir         string
	Hash            string
	Conv            conventionsFile
	Phase1Prompt    string
	Phase2Prompt    string
	Phase1Mock      string
	Phase2MockWrong string
	Variants        []LoaderVariant
	CanonicalTests  map[string][]byte
}

// LoaderVariant is the public variant type users of the fixture see. It is a
// separate name so both the on-disk and loaded representations are explicit.
type LoaderVariant = LoadedVariant

// loadFixtures discovers and validates all stress fixtures under dir. When want
// is non-empty only those task names are loaded (in the requested order).
func loadFixtures(dir string, want []string) ([]*Fixture, error) {
	names, err := discoverTaskNames(dir)
	if err != nil {
		return nil, err
	}
	if len(want) > 0 {
		known := map[string]bool{}
		for _, n := range names {
			known[n] = true
		}
		var filtered []string
		for _, n := range want {
			if !known[n] {
				return nil, fmt.Errorf("unknown task %q (available: %s)", n, strings.Join(names, ", "))
			}
			filtered = append(filtered, n)
		}
		names = filtered
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no stress fixtures found under %s", dir)
	}
	out := make([]*Fixture, 0, len(names))
	for _, name := range names {
		fx, err := loadFixture(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("fixture %s: %w", name, err)
		}
		out = append(out, fx)
	}
	return out, nil
}

func discoverTaskNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read fixtures dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "conventions.json")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func loadFixture(dir string) (*Fixture, error) {
	convPath := filepath.Join(dir, "conventions.json")
	b, err := os.ReadFile(convPath)
	if err != nil {
		return nil, err
	}
	var conv conventionsFile
	if err := json.Unmarshal(b, &conv); err != nil {
		return nil, fmt.Errorf("parse %s: %w", convPath, err)
	}
	if conv.Task == "" || conv.Package == "" || conv.Phase1File == "" || conv.Phase2File == "" {
		return nil, fmt.Errorf("%s is missing task/package/phase files", convPath)
	}
	if len(conv.Variants) < 3 {
		return nil, fmt.Errorf("%s has %d variants, want at least 3", convPath, len(conv.Variants))
	}
	if conv.DecisionTitle == "" {
		return nil, fmt.Errorf("%s has no decision_title", convPath)
	}
	seen := map[string]bool{}
	for _, v := range conv.Variants {
		if v.ID == "" || v.Statement == "" || v.Phrase == "" {
			return nil, fmt.Errorf("%s has a variant without id/statement/phrase", convPath)
		}
		if seen[v.ID] {
			return nil, fmt.Errorf("%s has duplicate variant id %q", convPath, v.ID)
		}
		seen[v.ID] = true
		for name, p := range map[string]string{
			"hidden_test": v.HiddenTest, "mock_phase2": v.MockPhase2,
		} {
			if p == "" {
				return nil, fmt.Errorf("variant %s has no %s path", v.ID, name)
			}
		}
	}
	if len(conv.VisibleTests) == 0 {
		return nil, fmt.Errorf("%s declares no visible_tests", convPath)
	}

	fx := &Fixture{
		Name:           conv.Task,
		Dir:            dir,
		RepoDir:        filepath.Join(dir, "repo"),
		Conv:           conv,
		CanonicalTests: map[string][]byte{},
	}
	if info, err := os.Stat(fx.RepoDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("repo/ directory is missing")
	}
	read := func(rel string) (string, error) {
		p := filepath.Join(dir, rel)
		data, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", p, err)
		}
		return string(data), nil
	}
	if fx.Phase1Prompt, err = read(filepath.Join("prompts", "phase1.md")); err != nil {
		return nil, err
	}
	if fx.Phase2Prompt, err = read(filepath.Join("prompts", "phase2.md")); err != nil {
		return nil, err
	}
	for _, placeholder := range []string{"{{CONVENTION_ID}}", "{{CONVENTION_STATEMENT}}", "{{PERSIST_INSTRUCTIONS}}"} {
		if !strings.Contains(fx.Phase1Prompt, placeholder) {
			return nil, fmt.Errorf("prompts/phase1.md is missing the %s placeholder", placeholder)
		}
	}
	if fx.Phase1Mock, err = read(conv.MockPhase1); err != nil {
		return nil, err
	}
	if fx.Phase2MockWrong, err = read(conv.MockPhase2Wrong); err != nil {
		return nil, err
	}
	for _, name := range conv.VisibleTests {
		data, err := os.ReadFile(filepath.Join(fx.RepoDir, name))
		if err != nil {
			return nil, fmt.Errorf("visible test %s: %w", name, err)
		}
		fx.CanonicalTests[name] = data
	}
	for _, v := range conv.Variants {
		hidden, err := read(v.HiddenTest)
		if err != nil {
			return nil, err
		}
		correct, err := read(v.MockPhase2)
		if err != nil {
			return nil, err
		}
		fx.Variants = append(fx.Variants, LoadedVariant{
			ID: v.ID, Statement: v.Statement, Phrase: v.Phrase,
			HiddenTestPath: v.HiddenTest, HiddenTest: hidden,
			MockPhase2Path: v.MockPhase2, MockPhase2: correct,
		})
	}
	if fx.Hash, err = hashTree(dir); err != nil {
		return nil, err
	}
	return fx, nil
}

// hashTree returns a stable sha256 over every file path and content in dir.
func hashTree(dir string) (string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", dir, err)
	}
	sort.Strings(rels)
	h := sha256.New()
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fileSum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s\x00%s\n", rel, hex.EncodeToString(fileSum[:]))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// promptPhase1 renders the phase-1 prompt for one arm/variant.
func promptPhase1(fx *Fixture, v *LoaderVariant, arm string) string {
	persist := persistInstructions(fx, v, arm)
	out := fx.Phase1Prompt
	out = strings.ReplaceAll(out, "{{CONVENTION_ID}}", v.ID)
	out = strings.ReplaceAll(out, "{{CONVENTION_STATEMENT}}", v.Statement)
	out = strings.ReplaceAll(out, "{{PERSIST_INSTRUCTIONS}}", persist)
	return out
}

// persistInstructions tells the phase-1 agent how to make the convention
// durable. The harness never does this for the agent: recording the decision is
// the behaviour the benchmark measures.
func persistInstructions(fx *Fixture, v *LoaderVariant, arm string) string {
	parent := phase1TaskID
	title := fmt.Sprintf("%s: %s", fx.Conv.DecisionTitle, v.ID)
	if !isAnchorArm(arm) {
		return ("This arm has no durable session store. Implement phase 1 and finish; the\n" +
			"benchmark does not seed any session state for you. Nothing you write in\n" +
			"chat will exist for the next session.")
	}
	return fmt.Sprintf(`This session has Anchor durable state. Record the convention as a decision
node yourself, before you finish (the wrapper exports ANCHOR_WORKSPACE,
ANCHOR_NODE, ANCHOR_BIN and ANCHOR_CONTEXT_FILE):

    "$ANCHOR_BIN" node create --dir "$ANCHOR_WORKSPACE" \
      --id dec-%s-%s --type decision --parent %s \
      --title "%s" \
      --decision "%s" \
      --rationale "chosen in phase 1; the visible tests do not pin it down"

If this session runs under `+"`anchor wrap --prepare-handoff`"+`, the successor
handoff packet is prepared automatically when you exit, so the decision must be
recorded before that. Do not rely on chat history or on the visible tests.`,
		fx.Name, v.ID, parent, title, v.Statement)
}

// variantDecisionID is the node id phase-1 prompts ask the agent to use.
func variantDecisionID(fx *Fixture, v *LoaderVariant) string {
	return fmt.Sprintf("dec-%s-%s", fx.Name, v.ID)
}

// variantIDs returns the fixture's variant ids in declaration order.
func variantIDs(fx *Fixture) []string {
	out := make([]string, 0, len(fx.Variants))
	for _, v := range fx.Variants {
		out = append(out, v.ID)
	}
	return out
}
