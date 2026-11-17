package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRootForTest walks up from the package directory to the module root.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod above %s", dir)
		}
		dir = parent
	}
}

func fixturesDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRootForTest(t), "bench", "fixtures", "polyglot")
}

// makeExercise writes a minimal Python or Go exercise for loader tests.
func makeExercise(t *testing.T, root, lang, slug string) string {
	t.Helper()
	dir := filepath.Join(root, lang, slug)
	if err := os.MkdirAll(filepath.Join(dir, ".docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".docs", "instructions.md"), []byte("Implement "+slug+".\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	switch lang {
	case "python":
		writeTestFile(t, filepath.Join(dir, slug+".py"), "def run():\n    raise NotImplementedError\n")
		writeTestFile(t, filepath.Join(dir, slug+"_test.py"), "from "+slug+" import run\n\n\ndef test_run():\n    assert run()\n")
	case "go":
		writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/"+slug+"\n\ngo 1.23\n")
		writeTestFile(t, filepath.Join(dir, slug+".go"), "package main\n\nfunc Run() bool { return false }\n")
		writeTestFile(t, filepath.Join(dir, slug+"_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {\n\tif !Run() {\n\t\tt.Fatal(\"not implemented\")\n\t}\n}\n")
	default:
		t.Fatalf("unsupported lang %q", lang)
	}
	return dir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTasksBundledFixtures(t *testing.T) {
	tasks, info, err := loadTasks(fixturesDir(t), "", "all")
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2: %+v", len(tasks), tasks)
	}
	if info.Available != 2 || info.Capped {
		t.Fatalf("load info = %+v, want available=2 capped=false", info)
	}
	if got := []string{tasks[0].Slug, tasks[1].Slug}; got[0] != "leap" || got[1] != "two_fer" {
		t.Fatalf("task order = %v, want [leap two_fer] (sorted)", got)
	}
	for _, task := range tasks {
		if task.Lang != "python" {
			t.Fatalf("%s language = %q, want python", task.Slug, task.Lang)
		}
		if len(task.Hash) != 64 || strings.Trim(task.Hash, "0123456789abcdef") != "" {
			t.Fatalf("%s hash = %q, want sha256 hex", task.Slug, task.Hash)
		}
		if len(task.TestFiles) != 1 || task.TestFiles[0] != task.Slug+"_test.py" {
			t.Fatalf("%s test files = %v", task.Slug, task.TestFiles)
		}
		if !strings.HasPrefix(strings.Join(task.Acceptance, " "), "python3 -m pytest -q") {
			t.Fatalf("%s acceptance = %v", task.Slug, task.Acceptance)
		}
		if !strings.Contains(task.Instructions, task.Slug) && !strings.Contains(strings.ToLower(task.Instructions), "leap") {
			t.Fatalf("%s instructions look empty: %q", task.Slug, task.Instructions)
		}
	}
	if tasks[0].Hash == tasks[1].Hash {
		t.Fatal("distinct exercises must have distinct hashes")
	}
}

func TestLoadTasksFilters(t *testing.T) {
	root := fixturesDir(t)
	tasks, info, err := loadTasks(root, "python", "leap")
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Slug != "leap" {
		t.Fatalf("got %+v, want only leap", tasks)
	}
	if info.Available != 1 {
		t.Fatalf("available = %d, want 1", info.Available)
	}

	if _, _, err := loadTasks(root, "go", "all"); err == nil || !strings.Contains(err.Error(), "no exercises found for language \"go\"") {
		t.Fatalf("expected clear --lang error, got %v", err)
	}
	if _, _, err := loadTasks(root, "", "does-not-exist"); err == nil || !strings.Contains(err.Error(), "unknown task(s)") {
		t.Fatalf("expected clear --tasks error, got %v", err)
	}
	if _, _, err := loadTasks(t.TempDir(), "", "all"); err == nil || !strings.Contains(err.Error(), "no Aider-polyglot exercises found") {
		t.Fatalf("expected empty-dir error, got %v", err)
	}
	if _, _, err := loadTasks(filepath.Join(root, "missing"), "", "all"); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestLoadTasksAiderLayoutAndCap(t *testing.T) {
	root := t.TempDir()
	// Aider's layout nests exercises under exercises/practice.
	practice := filepath.Join(root, "python", "exercises", "practice")
	makeExercise(t, practice, "python", "alpha")
	makeExercise(t, practice, "python", "beta")

	tasks, info, err := loadTasks(root, "", "all")
	if err != nil {
		t.Fatalf("loadTasks: %v", err)
	}
	if len(tasks) != 2 || info.Capped {
		t.Fatalf("got %d tasks (capped=%v), want 2 uncapped", len(tasks), info.Capped)
	}

	// More than maxDefaultTasks exercises are capped for the default selection.
	many := t.TempDir()
	for i := 0; i < maxDefaultTasks+5; i++ {
		makeExercise(t, many, "python", fmt.Sprintf("exercise_%02d", i))
	}
	tasks, info, err = loadTasks(many, "", "all")
	if err != nil {
		t.Fatalf("loadTasks (many): %v", err)
	}
	if len(tasks) != maxDefaultTasks || !info.Capped || info.Available != maxDefaultTasks+5 {
		t.Fatalf("cap = %d tasks, info=%+v; want %d capped", len(tasks), info, maxDefaultTasks)
	}

	// An explicit --tasks selection is honoured even above the default cap.
	var slugs []string
	for i := 0; i < maxDefaultTasks+2; i++ {
		slugs = append(slugs, fmt.Sprintf("exercise_%02d", i))
	}
	tasks, info, err = loadTasks(many, "", strings.Join(slugs, ","))
	if err != nil {
		t.Fatalf("loadTasks (explicit): %v", err)
	}
	if len(tasks) != len(slugs) || info.Capped {
		t.Fatalf("explicit selection = %d tasks, capped=%v; want %d uncapped", len(tasks), info.Capped, len(slugs))
	}
}

func TestLoadTasksSingleExerciseRoot(t *testing.T) {
	dir := filepath.Join(fixturesDir(t), "python", "leap")
	tasks, _, err := loadTasks(dir, "", "all")
	if err != nil {
		t.Fatalf("loadTasks single exercise: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Slug != "leap" {
		t.Fatalf("got %+v, want the leap exercise", tasks)
	}
}

func TestLoadTasksIncompleteExercises(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "python", "broken")
	writeTestFile(t, filepath.Join(dir, ".docs", "instructions.md"), "write something\n")
	writeTestFile(t, filepath.Join(dir, "broken_test.py"), "def test_x():\n    assert True\n")
	if _, _, err := loadTasks(root, "", "all"); err == nil || !strings.Contains(err.Error(), "missing broken.py") {
		t.Fatalf("expected incomplete-exercise error mentioning broken.py, got %v", err)
	}
}

func TestLoadTasksGoLayout(t *testing.T) {
	root := t.TempDir()
	makeExercise(t, root, "go", "leapgo")

	tasks, info, err := loadTasks(root, "go", "all")
	if err != nil {
		t.Fatalf("loadTasks go: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Lang != "go" || tasks[0].Slug != "leapgo" {
		t.Fatalf("go task = %+v", tasks)
	}
	if info.Langs[0] != "go" {
		t.Fatalf("langs = %v", info.Langs)
	}
	if got := strings.Join(tasks[0].Acceptance, " "); got != "go test -count=1 ./..." {
		t.Fatalf("go acceptance = %q", got)
	}
	if len(tasks[0].TestFiles) != 1 || tasks[0].TestFiles[0] != "leapgo_test.go" {
		t.Fatalf("go test files = %v", tasks[0].TestFiles)
	}
	if len(tasks[0].Hash) != 64 {
		t.Fatalf("go task hash = %q", tasks[0].Hash)
	}
	if _, _, err := loadTasks(root, "python", "all"); err == nil || !strings.Contains(err.Error(), "languages present: go") {
		t.Fatalf("expected python-filter error naming go, got %v", err)
	}
}

func TestLoadTasksKebabCasePythonSlug(t *testing.T) {
	// Exercism/Aider use a kebab-case exercise directory with snake_case
	// Python modules and tests.
	root := t.TempDir()
	dir := filepath.Join(root, "python", "exercises", "practice", "two-fer")
	writeTestFile(t, filepath.Join(dir, ".docs", "instructions.md"), "Return the two-fer sentence.\n")
	writeTestFile(t, filepath.Join(dir, "two_fer.py"), "def two_fer(name=\"you\"):\n    raise NotImplementedError\n")
	writeTestFile(t, filepath.Join(dir, "two_fer_test.py"), "from two_fer import two_fer\n\n\ndef test_default():\n    assert two_fer() == \"One for you, one for me.\"\n")
	tasks, _, err := loadTasks(root, "python", "two-fer")
	if err != nil {
		t.Fatalf("loadTasks kebab-case: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	task := tasks[0]
	if task.Slug != "two-fer" || task.Lang != "python" {
		t.Fatalf("task identity = %+v", task)
	}
	if len(task.SourceFiles) != 1 || task.SourceFiles[0] != "two_fer.py" {
		t.Fatalf("source files = %v, want [two_fer.py]", task.SourceFiles)
	}
	if len(task.TestFiles) != 1 || task.TestFiles[0] != "two_fer_test.py" {
		t.Fatalf("test files = %v", task.TestFiles)
	}
}
