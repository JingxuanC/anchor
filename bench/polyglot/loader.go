package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Task is one loaded exercise.
type Task struct {
	Slug         string   `json:"slug"`
	Lang         string   `json:"lang"`
	Dir          string   `json:"dir"`
	Hash         string   `json:"hash"`
	TestFiles    []string `json:"test_files"`
	SourceFiles  []string `json:"source_files,omitempty"`
	Acceptance   []string `json:"acceptance"`
	Instructions string   `json:"-"`
}

// LoadInfo records what the loader saw before/after the default task cap.
type LoadInfo struct {
	Available int      // exercises matching --lang/--tasks before the cap
	Capped    bool     // default selection was capped at maxDefaultTasks
	Langs     []string // languages present in the directory (before --lang)
	Warnings  []string
}

// skipDirs are directories that never belong to the agent's project or to the
// exercise identity hash.
var skipDirs = map[string]bool{
	".git":          true,
	".anchor":       true,
	"mock":          true,
	"__pycache__":   true,
	".pytest_cache": true,
	"node_modules":  true,
}

// exercisePath is used when the loader is pointed directly at one exercise
// instead of a polyglot root.
func isExerciseDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".docs", "instructions.md"))
	return err == nil && !info.IsDir()
}

// loadTasks discovers exercises under root and applies the --lang and --tasks
// filters. Unsupported languages are skipped; a directory that clearly claims to
// be a Python/Go exercise but is incomplete produces a warning (and an error if
// no usable exercise remains).
func loadTasks(root, langFilter, taskFilter string) ([]Task, LoadInfo, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, LoadInfo{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, LoadInfo{}, fmt.Errorf("polyglot directory %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, LoadInfo{}, fmt.Errorf("polyglot directory %s is not a directory", root)
	}

	var tasks []Task
	li := LoadInfo{}

	add := func(dir string) {
		task, err := detectExercise(dir)
		if err != nil {
			li.Warnings = append(li.Warnings, err.Error())
			return
		}
		if task != nil {
			tasks = append(tasks, *task)
		}
	}

	if isExerciseDir(root) {
		add(root)
	} else {
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if path != root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			if !isExerciseDir(path) {
				return nil
			}
			add(path)
			// A found exercise is a leaf: do not descend into its subdirectories
			// looking for nested exercises (it can only produce duplicates).
			return fs.SkipDir
		})
		if walkErr != nil {
			return nil, li, fmt.Errorf("walk %s: %w", root, walkErr)
		}
	}
	if len(tasks) == 0 {
		detail := ""
		if len(li.Warnings) > 0 {
			detail = ": " + strings.Join(li.Warnings, "; ")
		}
		return nil, li, fmt.Errorf("no Aider-polyglot exercises found in %s (expected <lang>/<slug>/.docs/instructions.md plus python or go test files, or Aider's <lang>/exercises/practice/<slug>/ layout)%s", root, detail)
	}

	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Lang != tasks[j].Lang {
			return tasks[i].Lang < tasks[j].Lang
		}
		return tasks[i].Slug < tasks[j].Slug
	})
	langSet := map[string]bool{}
	for _, t := range tasks {
		langSet[t.Lang] = true
	}
	for lang := range langSet {
		li.Langs = append(li.Langs, lang)
	}
	sort.Strings(li.Langs)

	filtered := tasks
	if langFilter != "" {
		var keep []Task
		for _, t := range filtered {
			if t.Lang == langFilter {
				keep = append(keep, t)
			}
		}
		filtered = keep
		if len(filtered) == 0 {
			return nil, li, fmt.Errorf("no exercises found for language %q under %s (languages present: %s)",
				langFilter, root, strings.Join(li.Langs, ", "))
		}
	}

	selectAll := true
	var wanted []string
	if strings.TrimSpace(taskFilter) != "" && !strings.EqualFold(strings.TrimSpace(taskFilter), "all") {
		selectAll = false
		for _, part := range strings.Split(taskFilter, ",") {
			slug := strings.TrimSpace(part)
			if slug == "" {
				continue
			}
			wanted = append(wanted, slug)
		}
		if len(wanted) == 0 {
			return nil, li, fmt.Errorf("--tasks contains no task slugs")
		}
	}
	if !selectAll {
		bySlug := map[string]Task{}
		for _, t := range filtered {
			bySlug[t.Slug] = t
		}
		var selected []Task
		var unknown []string
		for _, slug := range wanted {
			t, ok := bySlug[slug]
			if !ok {
				unknown = append(unknown, slug)
				continue
			}
			selected = append(selected, t)
		}
		if len(unknown) > 0 {
			var available []string
			for _, t := range filtered {
				available = append(available, t.Slug)
			}
			return nil, li, fmt.Errorf("unknown task(s) %s; available tasks: %s",
				quoteList(unknown), strings.Join(available, ", "))
		}
		filtered = selected
	}

	li.Available = len(filtered)
	if selectAll && len(filtered) > maxDefaultTasks {
		filtered = filtered[:maxDefaultTasks]
		li.Capped = true
	}
	return filtered, li, nil
}

// detectExercise classifies one directory. It returns (nil, nil) when the
// directory is not an exercise or uses an unsupported language, and a non-nil
// error when it looks like a Python/Go exercise but is incomplete.
func detectExercise(dir string) (*Task, error) {
	slug := filepath.Base(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	names := map[string]bool{}
	var pyTests, pySources, goTests []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		names[name] = true
		if strings.HasSuffix(name, "_test.py") || (strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py")) {
			pyTests = append(pyTests, name)
		}
		if strings.HasSuffix(name, ".py") && !strings.HasSuffix(name, "_test.py") &&
			!strings.HasPrefix(name, "test_") && name != "conftest.py" && name != "setup.py" {
			pySources = append(pySources, name)
		}
		if strings.HasSuffix(name, "_test.go") {
			goTests = append(goTests, name)
		}
	}
	sort.Strings(pyTests)
	sort.Strings(pySources)
	sort.Strings(goTests)

	instructionsPath := filepath.Join(dir, ".docs", "instructions.md")
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", dir, instructionsPath, err)
	}

	switch {
	case len(pyTests) > 0 || len(pySources) > 0 || names[slug+".py"]:
		// Exercism/Aider directory slugs are kebab-case ("two-fer") while the
		// Python module and test files are snake_case ("two_fer.py"), so accept
		// either spelling and fall back to the only non-test source file.
		stub := pythonStubName(slug, pySources)
		if stub == "" {
			candidates := []string{slug + ".py"}
			if normalized := strings.ReplaceAll(slug, "-", "_") + ".py"; normalized != candidates[0] {
				candidates = append(candidates, normalized)
			}
			return nil, fmt.Errorf("%s looks like a python exercise but is missing %s", dir, strings.Join(candidates, " or "))
		}
		if len(pyTests) == 0 {
			return nil, fmt.Errorf("%s looks like a python exercise but has no *_test.py file", dir)
		}
		acceptance := append([]string{"python3", "-m", "pytest", "-q"}, pyTests...)
		return &Task{
			Slug:         slug,
			Lang:         "python",
			Dir:          dir,
			Hash:         hashExercise(dir),
			TestFiles:    pyTests,
			SourceFiles:  []string{stub},
			Acceptance:   acceptance,
			Instructions: string(instructions),
		}, nil
	case len(goTests) > 0 || names["go.mod"]:
		if !names["go.mod"] {
			return nil, fmt.Errorf("%s looks like a go exercise but is missing go.mod", dir)
		}
		if len(goTests) == 0 {
			return nil, fmt.Errorf("%s looks like a go exercise but has no *_test.go file", dir)
		}
		var sources []string
		for name := range names {
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				sources = append(sources, name)
			}
		}
		sort.Strings(sources)
		return &Task{
			Slug:         slug,
			Lang:         "go",
			Dir:          dir,
			Hash:         hashExercise(dir),
			TestFiles:    goTests,
			SourceFiles:  sources,
			Acceptance:   []string{"go", "test", "-count=1", "./..."},
			Instructions: string(instructions),
		}, nil
	default:
		return nil, nil
	}
}

// hashExercise hashes the exercise's own files (instructions, sources, tests)
// excluding harness-only directories such as mock/. The hash is recorded in the
// report so a run can be tied to exact fixture content.
// pythonStubName resolves the implementation module for a Python exercise. It
// prefers the directory slug, then the snake_case normalisation, then the only
// non-test .py file in the directory.
func pythonStubName(slug string, pySources []string) string {
	candidates := []string{slug + ".py"}
	if normalized := strings.ReplaceAll(slug, "-", "_") + ".py"; normalized != candidates[0] {
		candidates = append(candidates, normalized)
	}
	for _, candidate := range candidates {
		for _, source := range pySources {
			if source == candidate {
				return candidate
			}
		}
	}
	if len(pySources) == 1 {
		return pySources[0]
	}
	return ""
}

func hashExercise(dir string) string {
	type fileEntry struct {
		rel  string
		data []byte
	}
	var files []fileEntry
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		files = append(files, fileEntry{rel: filepath.ToSlash(rel), data: data})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.rel, len(f.data))
		h.Write(f.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func quoteList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return strings.Join(quoted, ", ")
}
