package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillMarkdownMatchesRepoCopy keeps the embedded skill and the browsable
// skills/anchor/SKILL.md byte-identical.
func TestSkillMarkdownMatchesRepoCopy(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "anchor", "SKILL.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skills/anchor/SKILL.md missing: %v", err)
	}
	if string(b) != SkillMarkdown {
		t.Fatalf("%s is out of sync with internal/adapter/assets/skill.md", path)
	}
}

func TestSkillMarkdownShape(t *testing.T) {
	if !strings.HasPrefix(SkillMarkdown, "---\nname: anchor\n") {
		t.Fatal("skill must start with YAML frontmatter and name: anchor")
	}
	for _, want := range []string{"description:", "# Using Anchor", "anchor tree", "handoff prepare", "handoff restore", "anchor mcp", "ANCHOR_CONTEXT_FILE", "Do not fabricate provenance"} {
		if !strings.Contains(SkillMarkdown, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}
