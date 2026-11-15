package core

import (
	"fmt"
	"regexp"
)

type secretPattern struct {
	name string
	re   *regexp.Regexp
}

// These are deliberately narrow, high-confidence signatures. The scanner is a
// guardrail for explicitly supplied artifacts, not a claim that arbitrary
// content is safe or that all credential formats are covered.
var secretPatterns = []secretPattern{
	{"private key block", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)},
	{"API secret token", regexp.MustCompile(`\b(?:sk|pk)[-_](?:ant-|live-|test-)?[A-Za-z0-9_-]{16,}\b`)},
	{"credential assignment", regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|secret|password|passwd)\b\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{8,}`)},
}

// ScanSecrets rejects recognizable secret material without echoing the matched
// value. Callers must not include the rejected bytes in diagnostics.
func ScanSecrets(data []byte) error {
	for _, p := range secretPatterns {
		if p.re.Match(data) {
			return fmt.Errorf("potential secret detected (%s)", p.name)
		}
	}
	return nil
}

func scanEvidenceMetadata(values ...string) error {
	for _, v := range values {
		if err := ScanSecrets([]byte(v)); err != nil {
			return fmt.Errorf("evidence metadata rejected: %w", err)
		}
	}
	return nil
}
