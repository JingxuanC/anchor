package adapter

import _ "embed"

// SkillMarkdown is the canonical Anchor agent skill. It is embedded so
// `anchor skill print` and `anchor skill install` work from an installed binary.
// skills/anchor/SKILL.md is a byte-identical copy for GitHub/browsing, kept in
// sync by a test.
//
//go:embed assets/skill.md
var SkillMarkdown string
