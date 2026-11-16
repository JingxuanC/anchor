package adapter

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DefaultDetectTimeout bounds each <cli> --version probe. A missing or hanging
// binary must never block the CLI.
const DefaultDetectTimeout = 2 * time.Second

// binaryFor maps an adapter id to the command that reports its version.
var binaryFor = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"gemini-cli":  "gemini",
}

// Detection is the honest, read-only result of probing an installed agent CLI.
// It never upgrades the hook evidence: a detectable version proves the CLI
// exists, not that its hooks were exercised.
type Detection struct {
	AdapterID        string `json:"adapter_id"`
	Binary           string `json:"binary"`
	Installed        bool   `json:"installed"`
	Version          string `json:"version,omitempty"`
	RawVersion       string `json:"raw_version,omitempty"`
	NativeHookStatus string `json:"native_hook_status"`
	EndToEndVerified bool   `json:"end_to_end_verified"`
	Error            string `json:"error,omitempty"`
}

var versionPattern = regexp.MustCompile(`(?:^|[^0-9.])(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)`)

// ParseVersion extracts the first semantic version from version output. Both
// "2.1.252 (Claude Code)" and "codex-cli 0.141.0" are accepted.
func ParseVersion(output string) string {
	if m := versionPattern.FindStringSubmatch(output); len(m) == 2 {
		return m[1]
	}
	return ""
}

// Detect probes one adapter binary. It uses argv (never a shell) and a context
// timeout, and records errors instead of failing the caller.
func Detect(adapterID string, timeout time.Duration) Detection {
	binary, ok := binaryFor[adapterID]
	if !ok {
		return Detection{AdapterID: adapterID, Installed: false, NativeHookStatus: "unverified", Error: "unknown adapter"}
	}
	d := Detection{AdapterID: adapterID, Binary: binary, NativeHookStatus: "unverified", EndToEndVerified: false}
	if timeout <= 0 {
		timeout = DefaultDetectTimeout
	}
	if _, err := exec.LookPath(binary); err != nil {
		d.Error = binary + " is not installed or not on PATH"
		return d
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	out, err := cmd.CombinedOutput()
	raw := strings.TrimSpace(string(out))
	d.RawVersion = raw
	if ctx.Err() == context.DeadlineExceeded {
		d.Error = binary + " --version timed out"
		return d
	}
	if err != nil && raw == "" {
		d.Error = binary + " --version failed: " + err.Error()
		return d
	}
	if v := ParseVersion(raw); v != "" {
		d.Version = v
	}
	d.Installed = true
	if d.Version == "" {
		// The binary answered but did not expose a semver; keep the raw text.
		d.Error = "version output could not be parsed"
	}
	return d
}

// DetectAll probes every known adapter in a deterministic order.
func DetectAll(timeout time.Duration) []Detection {
	ids := make([]string, 0, len(binaryFor))
	for id := range binaryFor {
		ids = append(ids, id)
	}
	// Deterministic order regardless of map iteration.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	out := make([]Detection, 0, len(ids))
	for _, id := range ids {
		out = append(out, Detect(id, timeout))
	}
	return out
}
