package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writeReport writes report.json and report.md into outDir.
func writeReport(outDir string, report *Report) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	jsonPath := filepath.Join(outDir, "report.json")
	mdPath := filepath.Join(outDir, "report.md")
	report.Artifacts = ReportArtifacts{
		Root:       outDir,
		ReportJSON: jsonPath,
		ReportMD:   mdPath,
		TrialDirs:  len(report.Trials),
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(jsonPath, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write report.json: %w", err)
	}
	if err := os.WriteFile(mdPath, []byte(reportMarkdown(report)), 0o644); err != nil {
		return fmt.Errorf("write report.md: %w", err)
	}
	return nil
}

// cliSummary renders the human-readable summary printed after a run.
func cliSummary(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s v%s\n", report.Suite, report.SuiteVersion)
	fmt.Fprintf(&b, "  generated: %s\n", report.GeneratedAt)
	fmt.Fprintf(&b, "  agent:     %s\n", agentDescription(report))
	fmt.Fprintf(&b, "  anchor:    %s\n", report.Metadata.AnchorVersion)
	fmt.Fprintf(&b, "  go:        %s\n", report.Metadata.GoVersion)
	fmt.Fprintf(&b, "  seed:      %d  tasks: %s  arms: %s\n", report.Config.Seed,
		strings.Join(report.Config.Tasks, ","), strings.Join(report.Config.Arms, ","))
	fmt.Fprintf(&b, "  trials:    %d/%d", report.Budget.TrialsRun, report.Budget.TrialsPlanned)
	if report.Truncated {
		b.WriteString("  (TRUNCATED)")
	}
	b.WriteString("\n")
	if report.Budget.MaxTokens > 0 || report.Budget.MaxUSD > 0 {
		fmt.Fprintf(&b, "  budget:    tokens=%d cost=$%.6f (caps: max_tokens=%d max_usd=%g)\n",
			report.Budget.TokensUsed, report.Budget.CostUSD, report.Budget.MaxTokens, report.Budget.MaxUSD)
	} else {
		fmt.Fprintf(&b, "  spend:     tokens=%d cost=$%.6f\n", report.Budget.TokensUsed, report.Budget.CostUSD)
	}
	b.WriteString("\n")
	b.WriteString(armTable(report))
	b.WriteString("\n")
	if len(report.PerTask) > 0 {
		b.WriteString("phase-2 pass by task (pass@1):\n")
		b.WriteString(perTaskTable(report))
		b.WriteString("\n")
	}
	b.WriteString("variant assignment:\n")
	for _, task := range report.Config.Tasks {
		counts := report.VariantsUsed[task]
		var parts []string
		for _, v := range sortedKeys(counts) {
			parts = append(parts, fmt.Sprintf("%s=%d", v, counts[v]))
		}
		if len(parts) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %-8s %s\n", task, strings.Join(parts, " "))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "artifacts: %s\n", report.Artifacts.Root)
	fmt.Fprintf(&b, "           %s\n", report.Artifacts.ReportJSON)
	fmt.Fprintf(&b, "           %s\n", report.Artifacts.ReportMD)
	for _, limitation := range report.Limitations {
		if strings.Contains(limitation, "Mock mode") && report.Config.MockAgent {
			fmt.Fprintf(&b, "note: %s\n", limitation)
			break
		}
	}
	return b.String()
}

func agentDescription(report *Report) string {
	if report.Config.MockAgent {
		return "mock (offline self-test; modes: " + strings.Join(report.Config.MockModes, ",") + ")"
	}
	desc := report.Config.AgentCmd
	if report.Config.AgentModel != "" {
		desc += " (model " + report.Config.AgentModel + ")"
	}
	return desc
}

func armTable(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-9s %6s %11s %11s %18s %14s %11s %9s %10s %10s %10s %14s\n",
		"arm", "trials", "phase1@1", "phase2@1", "phase2 95% CI", "pass^k", "tokens", "cost", "decisions", "packetdec", "leaked", "conveyed")
	for _, arm := range report.Arms {
		a := report.Aggregates[arm]
		if a == nil {
			continue
		}
		fmt.Fprintf(&b, "%-9s %6d %11s %11s %18s %14s %11.0f %9.5f %10d %10d %10d %14d\n",
			a.Arm, a.Trials, fmtPct(a.Phase1PassAt1), fmtPct(a.Phase2PassAt1),
			fmtCI(a.Phase2CI),
			fmt.Sprintf("%s(k=%d)", fmtPct(a.Phase2PassK), a.PassK),
			a.MeanTokens, a.MeanCostUSD,
			a.DecisionsRecorded, a.PacketDecisions, a.VariantLeaks, a.VariantConveyed)
	}
	return b.String()
}

func perTaskTable(report *Report) string {
	var b strings.Builder
	for _, task := range orderedTasks(report.PerTask) {
		ts := report.PerTask[task]
		var arms []string
		for arm := range ts.Arms {
			arms = append(arms, arm)
		}
		sort.Slice(arms, func(i, j int) bool { return armRank(arms[i]) < armRank(arms[j]) })
		var parts []string
		for _, arm := range arms {
			stat := ts.Arms[arm]
			parts = append(parts, fmt.Sprintf("%s=%d/%d", arm, stat.Phase2Pass, stat.Trials))
		}
		fmt.Fprintf(&b, "  %-8s %s\n", task, strings.Join(parts, "  "))
	}
	return b.String()
}

func reportMarkdown(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s report\n\n", report.Suite)
	fmt.Fprintf(&b, "Suite version `%s`, generated `%s`.\n\n", report.SuiteVersion, report.GeneratedAt)

	b.WriteString("## Configuration\n\n")
	fmt.Fprintf(&b, "- trials per (task, arm): %d\n", report.Config.Trials)
	fmt.Fprintf(&b, "- tasks: %s\n", strings.Join(report.Config.Tasks, ", "))
	fmt.Fprintf(&b, "- arms: %s\n", strings.Join(report.Config.Arms, ", "))
	fmt.Fprintf(&b, "- variant seed: %d\n", report.Config.Seed)
	fmt.Fprintf(&b, "- agent: %s\n", agentDescription(report))
	fmt.Fprintf(&b, "- go: %s\n", report.Metadata.GoVersion)
	fmt.Fprintf(&b, "- anchor: %s (%s)\n", report.Metadata.AnchorVersion, report.Metadata.AnchorBin)
	fmt.Fprintf(&b, "- timeout per command: %d ms\n", report.Config.TimeoutMS)
	fmt.Fprintf(&b, "- trial projects kept: %v\n", report.Config.KeepWork)
	if report.Truncated {
		fmt.Fprintf(&b, "- **truncated**: %s\n", report.BudgetNote)
	}
	b.WriteString("\n### Task hashes\n\n")
	b.WriteString("| task | package | variants | sha256 |\n|---|---|---|---|\n")
	for _, task := range report.Tasks {
		fmt.Fprintf(&b, "| %s | %s | %s | `%s` |\n", task.Name, task.Package,
			strings.Join(task.Variants, ", "), shortHash(task.Hash))
	}
	b.WriteString("\n")

	b.WriteString("## Per-arm aggregates\n\n")
	b.WriteString("`pass@1` is the fraction of trials whose acceptance run passed. `95% CI` is a Wilson score interval. `pass^k` is the combinatorial estimator C(c,k)/C(n,k) for k independent trials (k = trials per task).\n\n")
	b.WriteString("| arm | trials | phase1 pass@1 | phase2 pass@1 | phase2 95% CI | phase2 pass^k | mean tokens | mean cost USD | decisions recorded | packet decisions | variant leaked in repo | variant conveyed | tests modified | handoff verified |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, arm := range report.Arms {
		a := report.Aggregates[arm]
		if a == nil {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s (k=%d) | %.0f | %.6f | %d | %d | %d | %d | %d | %d |\n",
			a.Arm, a.Trials, fmtPct(a.Phase1PassAt1), fmtPct(a.Phase2PassAt1), fmtCI(a.Phase2CI),
			fmtPct(a.Phase2PassK), a.PassK, a.MeanTokens, a.MeanCostUSD,
			a.DecisionsRecorded, a.PacketDecisions, a.VariantLeaks, a.VariantConveyed, a.TestsModified, a.HandoffVerified)
	}
	b.WriteString("\n")

	b.WriteString("## Phase-2 results by task\n\n")
	b.WriteString("| task | arm | trials | phase2 pass | phase2 pass@1 |\n|---|---|---|---|---|\n")
	for _, task := range orderedTasks(report.PerTask) {
		ts := report.PerTask[task]
		var arms []string
		for arm := range ts.Arms {
			arms = append(arms, arm)
		}
		sort.Slice(arms, func(i, j int) bool { return armRank(arms[i]) < armRank(arms[j]) })
		for _, arm := range arms {
			stat := ts.Arms[arm]
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %s |\n", task, arm, stat.Trials, stat.Phase2Pass, fmtPct(stat.Phase2PassAt1))
		}
	}
	b.WriteString("\n")

	b.WriteString("## Variant assignment\n\n")
	b.WriteString("The phase-1 prompt always states the chosen variant; the visible tests never pin it down. Assignment is a seeded shuffled rotation, so every variant is exercised about equally.\n\n")
	b.WriteString("| task | variant | trials |\n|---|---|---|\n")
	for _, task := range report.Config.Tasks {
		counts := report.VariantsUsed[task]
		for _, v := range sortedKeys(counts) {
			fmt.Fprintf(&b, "| %s | %s | %d |\n", task, v, counts[v])
		}
	}
	b.WriteString("\n")

	b.WriteString("## Compliance\n\n")
	b.WriteString("`decisions_recorded` counts trials where an Anchor decision node carrying the variant was found after phase 1 (the phase-1 agent must have recorded it; the harness never seeds decisions). `packet_decisions` counts packet decisions carrying the variant. `variant_conveyed` reports whether the phase-2 context actually carried the variant id or statement. These are measured, not assumed.\n\n")

	b.WriteString("## Budget\n\n")
	fmt.Fprintf(&b, "- trials run: %d of %d planned\n", report.Budget.TrialsRun, report.Budget.TrialsPlanned)
	fmt.Fprintf(&b, "- tokens used: %d (cap: %d)\n", report.Budget.TokensUsed, report.Budget.MaxTokens)
	fmt.Fprintf(&b, "- cost reported: $%.6f (cap: $%g)\n", report.Budget.CostUSD, report.Budget.MaxUSD)
	fmt.Fprintf(&b, "- truncated: %v\n\n", report.Truncated)

	b.WriteString("## Artifacts\n\n")
	fmt.Fprintf(&b, "Per-trial artifacts (prompts, agent/acceptance logs, patches, handoff packets, hidden tests, mock plans) live under `%s/trials/`.\n\n", report.Artifacts.Root)
	b.WriteString("- report.json — machine-readable report\n- report.md — this file\n\n")

	b.WriteString("## Limitations\n\n")
	for _, limitation := range report.Limitations {
		fmt.Fprintf(&b, "- %s\n", limitation)
	}
	return b.String()
}

func shortHash(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:16] + "…"
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
