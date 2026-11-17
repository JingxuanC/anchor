package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Report is the machine-readable benchmark result written to report.json.
type Report struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   string                  `json:"generated_at"`
	Config        ReportConfig            `json:"config"`
	Metadata      Metadata                `json:"metadata"`
	Budget        Budget                  `json:"budget"`
	Trials        []TrialResult           `json:"trials"`
	PerTask       []TaskAggregate         `json:"per_task"`
	Aggregates    map[string]ArmAggregate `json:"aggregates"`
	Environment   []string                `json:"environment_notes"`
	Artifacts     Artifacts               `json:"artifacts"`
	Skipped       bool                    `json:"skipped,omitempty"`
	SkipReason    string                  `json:"skip_reason,omitempty"`
}

// ReportConfig records the resolved run settings.
type ReportConfig struct {
	PolyglotDir string  `json:"polyglot_dir"`
	Lang        string  `json:"lang,omitempty"`
	Tasks       string  `json:"tasks"`
	Trials      int     `json:"trials"`
	Arm         string  `json:"arm"`
	MockAgent   bool    `json:"mock_agent"`
	MockMode    string  `json:"mock_mode,omitempty"`
	AgentCmd    string  `json:"agent_cmd,omitempty"`
	AgentModel  string  `json:"agent_model,omitempty"`
	TimeoutMS   int64   `json:"timeout_ms"`
	MaxUSD      float64 `json:"max_usd"`
	MaxTokens   int64   `json:"max_tokens"`
	OutDir      string  `json:"out_dir"`
	AnchorBin   string  `json:"anchor_bin,omitempty"`
}

// Metadata records provenance: fixture identity, environment, tool versions.
type Metadata struct {
	GoVersion           string         `json:"go_version"`
	RepoRoot            string         `json:"repo_root,omitempty"`
	RepoCommit          string         `json:"repo_commit,omitempty"`
	RepoDirty           bool           `json:"repo_dirty,omitempty"`
	AnchorVersion       string         `json:"anchor_version,omitempty"`
	AnchorAvailable     bool           `json:"anchor_available"`
	AnchorWrapSupported bool           `json:"anchor_wrap_supported"`
	AnchorError         string         `json:"anchor_error,omitempty"`
	AgentModel          string         `json:"agent_model,omitempty"`
	PythonAvailable     bool           `json:"python_available"`
	PytestAvailable     bool           `json:"pytest_available"`
	PythonVersion       string         `json:"python_version,omitempty"`
	PythonNote          string         `json:"python_note,omitempty"`
	TaskCount           int            `json:"task_count"`
	TasksAvailable      int            `json:"tasks_available"`
	TasksCapped         bool           `json:"tasks_capped"`
	TaskWarnings        []string       `json:"task_warnings,omitempty"`
	Tasks               []TaskMetadata `json:"tasks"`
}

// TaskMetadata ties the run to exact fixture content.
type TaskMetadata struct {
	Slug       string   `json:"slug"`
	Lang       string   `json:"lang"`
	Hash       string   `json:"hash"`
	Dir        string   `json:"dir"`
	TestFiles  []string `json:"test_files"`
	Acceptance []string `json:"acceptance"`
}

// TrialResult is one (task, trial, arm) observation.
type TrialResult struct {
	Task                 string     `json:"task"`
	Lang                 string     `json:"lang"`
	Trial                int        `json:"trial"`
	Arm                  string     `json:"arm"`
	Pass                 bool       `json:"pass"`
	TestsModified        bool       `json:"tests_modified"`
	CheatFlags           []string   `json:"cheat_flags"`
	WallMS               int64      `json:"wall_ms"`
	AgentMS              int64      `json:"agent_ms"`
	AcceptMS             int64      `json:"accept_ms"`
	AgentExit            int        `json:"agent_exit"`
	AcceptExit           int        `json:"accept_exit"`
	AgentTimedOut        bool       `json:"agent_timed_out,omitempty"`
	AcceptanceSkipped    bool       `json:"acceptance_skipped,omitempty"`
	AcceptanceSkipReason string     `json:"acceptance_skip_reason,omitempty"`
	Tokens               int64      `json:"tokens"`
	CostUSD              float64    `json:"cost_usd"`
	Usage                TokenUsage `json:"usage"`
	AgentModel           string     `json:"agent_model,omitempty"`
	WrapUsed             bool       `json:"wrap_used,omitempty"`
	WrapFallback         string     `json:"wrap_fallback,omitempty"`
	PatchBytes           int        `json:"patch_bytes"`
	PatchFiles           int        `json:"patch_files"`
	FixtureCommit        string     `json:"fixture_commit,omitempty"`
	AnchorStateDir       string     `json:"anchor_state_dir,omitempty"`
	AnchorNodeID         string     `json:"anchor_node_id,omitempty"`
	WorkDir              string     `json:"work_dir,omitempty"`
	LogDir               string     `json:"log_dir"`
	Errors               []string   `json:"errors,omitempty"`
}

// WilsonCI is a 95% Wilson score interval.
type WilsonCI struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// TaskAggregate is the per-task table entry: one row set per arm.
type TaskAggregate struct {
	Task         string                  `json:"task"`
	Lang         string                  `json:"lang"`
	TrialsPerArm int                     `json:"trials_per_arm"`
	Arms         map[string]TaskArmStats `json:"arms"`
}

// TaskArmStats is one (task, arm) cell of the per-task table.
type TaskArmStats struct {
	Trials        int      `json:"trials"`
	Passed        int      `json:"passed"`
	Pass1         float64  `json:"pass_at_1"`
	Pass1CI       WilsonCI `json:"pass_at_1_wilson_95"`
	PassK         bool     `json:"pass_at_k"`
	MeanWallMS    float64  `json:"mean_wall_ms"`
	MeanTokens    float64  `json:"mean_tokens"`
	MeanCostUSD   float64  `json:"mean_cost_usd"`
	TestsModified int      `json:"tests_modified"`
}

// ArmAggregate summarises one arm across all tasks and trials.
type ArmAggregate struct {
	Arm           string   `json:"arm"`
	Tasks         int      `json:"tasks"`
	Trials        int      `json:"trials"`
	TrialsPerTask int      `json:"trials_per_task"`
	Passed        int      `json:"passed"`
	Pass1         float64  `json:"pass_at_1"`
	Pass1CI       WilsonCI `json:"pass_at_1_wilson_95"`
	PassK         float64  `json:"pass_at_k"`
	PassKTasks    int      `json:"pass_at_k_tasks"`
	PassKEligible int      `json:"pass_at_k_eligible_tasks"`
	MeanWallMS    float64  `json:"mean_wall_ms"`
	MeanTokens    float64  `json:"mean_tokens"`
	MeanCostUSD   float64  `json:"mean_cost_usd"`
	TestsModified int      `json:"tests_modified"`
}

// Budget records cost/token totals and whether a cap stopped the run early.
type Budget struct {
	MaxUSD         float64 `json:"max_usd"`
	MaxTokens      int64   `json:"max_tokens"`
	TotalUSD       float64 `json:"total_cost_usd"`
	TotalTokens    int64   `json:"total_tokens"`
	BudgetExceeded bool    `json:"budget_exceeded"`
	Truncated      bool    `json:"truncated"`
	Reason         string  `json:"truncation_reason,omitempty"`
}

// Add accumulates one observation's usage.
func (b *Budget) Add(tokens int64, cost float64) {
	b.TotalTokens += tokens
	b.TotalUSD += cost
}

// Exceeded reports whether a configured cap has been reached.
func (b *Budget) Exceeded() bool {
	if b.MaxUSD > 0 && b.TotalUSD >= b.MaxUSD {
		return true
	}
	if b.MaxTokens > 0 && b.TotalTokens >= b.MaxTokens {
		return true
	}
	return false
}

// Artifacts records where the report and logs were written.
type Artifacts struct {
	JSON     string `json:"report_json"`
	Markdown string `json:"report_markdown"`
	LogsDir  string `json:"logs_dir"`
}

// finalizeReport refreshes the derived parts of a report from the trial rows.
func finalizeReport(report *Report, tasks []Task, arms []string, cfg Config, budget *Budget) {
	report.Budget = *budget
	report.PerTask, report.Aggregates = aggregateTrials(report.Trials, tasks, arms, cfg.Trials)
	if report.Metadata.AgentModel == "" {
		report.Metadata.AgentModel = cfg.AgentModel
	}
	if report.Metadata.AgentModel == "" {
		for _, row := range report.Trials {
			if row.AgentModel != "" {
				report.Metadata.AgentModel = row.AgentModel
				break
			}
		}
	}
	report.Environment = environmentNotes(cfg, report.Metadata, budget)
	modified, skipped := 0, 0
	for _, row := range report.Trials {
		if row.TestsModified {
			modified++
		}
		if row.AcceptanceSkipped {
			skipped++
		}
	}
	if modified > 0 {
		report.Environment = append(report.Environment,
			fmt.Sprintf("%d observation(s) modified, deleted or added test files; the canonical tests were restored from the pristine exercise before grading and the rows are flagged.", modified))
	}
	if skipped > 0 {
		report.Environment = append(report.Environment,
			fmt.Sprintf("%d observation(s) have skipped acceptance runs and are counted as not passing.", skipped))
	}
}

func aggregateTrials(rows []TrialResult, tasks []Task, arms []string, trialsPerTask int) ([]TaskAggregate, map[string]ArmAggregate) {
	agg := map[string]ArmAggregate{}
	for _, arm := range arms {
		var armRows []TrialResult
		for _, row := range rows {
			if row.Arm == arm {
				armRows = append(armRows, row)
			}
		}
		a := ArmAggregate{Arm: arm, TrialsPerTask: trialsPerTask, Trials: len(armRows)}
		taskPass := map[string]int{}
		taskTrials := map[string]int{}
		var passed int
		for _, row := range armRows {
			taskTrials[row.Task]++
			if row.Pass {
				passed++
				taskPass[row.Task]++
			}
			if row.TestsModified {
				a.TestsModified++
			}
		}
		a.Tasks = len(taskTrials)
		a.Passed = passed
		if len(armRows) > 0 {
			a.Pass1 = float64(passed) / float64(len(armRows))
		}
		a.Pass1CI = wilsonCI(passed, len(armRows))
		for task, trials := range taskTrials {
			if trials != trialsPerTask {
				continue
			}
			a.PassKEligible++
			if taskPass[task] == trialsPerTask {
				a.PassKTasks++
			}
		}
		if a.PassKEligible > 0 {
			a.PassK = float64(a.PassKTasks) / float64(a.PassKEligible)
		}
		a.MeanWallMS = meanFloat(func() []float64 { return pluck(armRows, func(r TrialResult) float64 { return float64(r.WallMS) }) }())
		a.MeanTokens = meanFloat(pluck(armRows, func(r TrialResult) float64 { return float64(r.Tokens) }))
		a.MeanCostUSD = meanFloat(pluck(armRows, func(r TrialResult) float64 { return r.CostUSD }))
		agg[arm] = a
	}

	var perTask []TaskAggregate
	for _, task := range tasks {
		entry := TaskAggregate{Task: task.Slug, Lang: task.Lang, TrialsPerArm: trialsPerTask, Arms: map[string]TaskArmStats{}}
		found := false
		for _, arm := range arms {
			var armRows []TrialResult
			for _, row := range rows {
				if row.Task == task.Slug && row.Arm == arm {
					armRows = append(armRows, row)
				}
			}
			if len(armRows) == 0 {
				continue
			}
			found = true
			stats := TaskArmStats{Trials: len(armRows)}
			for _, row := range armRows {
				if row.Pass {
					stats.Passed++
				}
				if row.TestsModified {
					stats.TestsModified++
				}
			}
			stats.Pass1 = float64(stats.Passed) / float64(len(armRows))
			stats.Pass1CI = wilsonCI(stats.Passed, len(armRows))
			stats.PassK = len(armRows) == trialsPerTask && stats.Passed == stats.Trials
			stats.MeanWallMS = meanFloat(pluck(armRows, func(r TrialResult) float64 { return float64(r.WallMS) }))
			stats.MeanTokens = meanFloat(pluck(armRows, func(r TrialResult) float64 { return float64(r.Tokens) }))
			stats.MeanCostUSD = meanFloat(pluck(armRows, func(r TrialResult) float64 { return r.CostUSD }))
			entry.Arms[arm] = stats
		}
		if found {
			perTask = append(perTask, entry)
		}
	}
	return perTask, agg
}

func pluck(rows []TrialResult, f func(TrialResult) float64) []float64 {
	out := make([]float64, 0, len(rows))
	for _, row := range rows {
		out = append(out, f(row))
	}
	return out
}

// wilsonCI is the Wilson score interval for a binomial proportion at 95%
// confidence (z = 1.959963984540054).
func wilsonCI(successes, n int) WilsonCI {
	if n <= 0 {
		return WilsonCI{}
	}
	const z = 1.959963984540054
	nf := float64(n)
	p := float64(successes) / nf
	denom := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / denom
	margin := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / denom
	low := center - margin
	high := center + margin
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return WilsonCI{Low: low, High: high}
}

func meanFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

// ---------------------------------------------------------------------------
// Report writing

func writeReports(outDir string, report *Report) error {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(outDir, "report.json"), string(b)+"\n"); err != nil {
		return err
	}
	return writeFile(filepath.Join(outDir, "report.md"), renderMarkdown(report))
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func renderCLISummary(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Aider-polyglot benchmark: %d task(s), %d trial(s), arm=%s, mock=%v",
		report.Metadata.TaskCount, report.Config.Trials, report.Config.Arm, report.Config.MockAgent)
	if report.Metadata.TasksCapped {
		fmt.Fprintf(&b, " (task cap %d)", maxDefaultTasks)
	}
	b.WriteString("\n")
	if report.Skipped {
		fmt.Fprintf(&b, "SKIPPED: %s\n", report.SkipReason)
	}
	fmt.Fprintf(&b, "%-9s %6s %7s %9s %6s %11s %9s %9s %7s\n",
		"arm", "trials", "pass@1", "95% CI", "pass@k", "mean wall ms", "tokens", "cost USD", "cheats")
	for _, arm := range []string{armBaseline, armAnchor} {
		a, ok := report.Aggregates[arm]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%-9s %6d %7.2f %9s %6.2f %11.0f %9.0f %9.4f %7d\n",
			arm, a.Trials, a.Pass1, formatCI(a.Pass1CI), a.PassK,
			a.MeanWallMS, a.MeanTokens, a.MeanCostUSD, a.TestsModified)
	}
	b.WriteString("\nper task:\n")
	for _, t := range report.PerTask {
		for _, arm := range []string{armBaseline, armAnchor} {
			stats, ok := t.Arms[arm]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "  %-20s %-9s trials=%d pass@1=%.2f pass@k=%v tests_modified=%d\n",
				t.Task, arm, stats.Trials, stats.Pass1, stats.PassK, stats.TestsModified)
		}
	}
	fmt.Fprintf(&b, "budget: cost=%.4f USD tokens=%d exceeded=%v truncated=%v\n",
		report.Budget.TotalUSD, report.Budget.TotalTokens, report.Budget.BudgetExceeded, report.Budget.Truncated)
	fmt.Fprintf(&b, "report.json: %s\nreport.md:   %s\n", report.Artifacts.JSON, report.Artifacts.Markdown)
	return b.String()
}

func renderMarkdown(report *Report) string {
	var b strings.Builder
	b.WriteString("# Aider-polyglot benchmark report\n\n")
	fmt.Fprintf(&b, "- generated: %s\n", report.GeneratedAt)
	fmt.Fprintf(&b, "- polyglot dir: `%s`\n", report.Config.PolyglotDir)
	fmt.Fprintf(&b, "- tasks selected: %d (available after filters: %d)\n", report.Metadata.TaskCount, report.Metadata.TasksAvailable)
	if report.Config.Lang != "" {
		fmt.Fprintf(&b, "- language filter: %s\n", report.Config.Lang)
	}
	fmt.Fprintf(&b, "- task filter: %s\n", report.Config.Tasks)
	fmt.Fprintf(&b, "- arms: %s\n", report.Config.Arm)
	fmt.Fprintf(&b, "- trials per (task, arm): %d\n", report.Config.Trials)
	fmt.Fprintf(&b, "- agent: %s\n", agentDescription(report))
	if report.Metadata.AgentModel != "" {
		fmt.Fprintf(&b, "- agent model: %s\n", report.Metadata.AgentModel)
	}
	fmt.Fprintf(&b, "- anchor: version=%s wrap_supported=%v available=%v\n",
		orNone(report.Metadata.AnchorVersion), report.Metadata.AnchorWrapSupported, report.Metadata.AnchorAvailable)
	if report.Metadata.AnchorError != "" {
		fmt.Fprintf(&b, "- anchor problem: %s\n", report.Metadata.AnchorError)
	}
	fmt.Fprintf(&b, "- python3: %v (%s); pytest: %v\n",
		report.Metadata.PythonAvailable, orNone(report.Metadata.PythonVersion), report.Metadata.PytestAvailable)
	fmt.Fprintf(&b, "- go: %s\n", report.Metadata.GoVersion)
	fmt.Fprintf(&b, "- repository commit: %s", orNone(report.Metadata.RepoCommit))
	if report.Metadata.RepoDirty {
		b.WriteString(" (working tree dirty)")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "- timeout per process: %d ms\n", report.Config.TimeoutMS)
	fmt.Fprintf(&b, "- budget caps: max_usd=%g max_tokens=%d\n\n", report.Config.MaxUSD, report.Config.MaxTokens)

	if report.Skipped {
		fmt.Fprintf(&b, "> **SKIPPED:** %s\n\n", report.SkipReason)
	}

	b.WriteString("## Results\n\n")
	b.WriteString("| arm | tasks | trials | pass@1 | 95% CI (pass@1) | pass@k | mean wall ms | mean tokens | mean cost USD | tests modified |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, arm := range []string{armBaseline, armAnchor} {
		a, ok := report.Aggregates[arm]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.2f | %s | %.2f (%d/%d tasks) | %.0f | %.0f | %.4f | %d |\n",
			arm, a.Tasks, a.Trials, a.Pass1, formatCI(a.Pass1CI), a.PassK, a.PassKTasks, a.PassKEligible,
			a.MeanWallMS, a.MeanTokens, a.MeanCostUSD, a.TestsModified)
	}

	b.WriteString("\n## Per-task results\n\n")
	b.WriteString("| task | language | arm | trials | pass@1 | 95% CI | pass@k (all trials pass) | mean wall ms | mean tokens | mean cost USD | tests modified |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range report.PerTask {
		for _, arm := range []string{armBaseline, armAnchor} {
			stats, ok := t.Arms[arm]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %.2f | %s | %v | %.0f | %.0f | %.4f | %d |\n",
				t.Task, t.Lang, arm, stats.Trials, stats.Pass1, formatCI(stats.Pass1CI), stats.PassK,
				stats.MeanWallMS, stats.MeanTokens, stats.MeanCostUSD, stats.TestsModified)
		}
	}

	b.WriteString("\n## Trial rows\n\n")
	b.WriteString("| task | trial | arm | pass | tests modified | wall ms | agent exit | accept exit | tokens | cost USD | log dir |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, row := range report.Trials {
		fmt.Fprintf(&b, "| %s | %d | %s | %v | %v | %d | %d | %d | %d | %.4f | `%s` |\n",
			row.Task, row.Trial, row.Arm, row.Pass, row.TestsModified, row.WallMS,
			row.AgentExit, row.AcceptExit, row.Tokens, row.CostUSD, filepath.Base(row.LogDir))
	}

	b.WriteString("\n## Budget\n\n")
	fmt.Fprintf(&b, "- max cost: %g USD\n", report.Budget.MaxUSD)
	fmt.Fprintf(&b, "- max tokens: %d\n", report.Budget.MaxTokens)
	fmt.Fprintf(&b, "- total cost: %.4f USD\n", report.Budget.TotalUSD)
	fmt.Fprintf(&b, "- total tokens: %d\n", report.Budget.TotalTokens)
	fmt.Fprintf(&b, "- budget_exceeded: %v\n", report.Budget.BudgetExceeded)
	fmt.Fprintf(&b, "- truncated: %v\n", report.Budget.Truncated)
	if report.Budget.Reason != "" {
		fmt.Fprintf(&b, "- reason: %s\n", report.Budget.Reason)
	}

	b.WriteString("\n## Tasks\n\n")
	b.WriteString("| task | language | sha256 | visible tests | acceptance command |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, t := range report.Metadata.Tasks {
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s | `%s` |\n",
			t.Slug, t.Lang, t.Hash, strings.Join(t.TestFiles, ", "), strings.Join(t.Acceptance, " "))
	}
	if len(report.Metadata.TaskWarnings) > 0 {
		b.WriteString("\nLoader warnings:\n\n")
		for _, w := range report.Metadata.TaskWarnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	b.WriteString("\n## Environment and honest caveats\n\n")
	for _, note := range report.Environment {
		fmt.Fprintf(&b, "- %s\n", note)
	}

	b.WriteString("\n## Artifacts\n\n")
	fmt.Fprintf(&b, "- report.json: `%s`\n", report.Artifacts.JSON)
	fmt.Fprintf(&b, "- report.md: `%s`\n", report.Artifacts.Markdown)
	fmt.Fprintf(&b, "- logs: `%s` (`<task>-t<N>-<arm>/{prompt.txt,patch.diff,agent.log,acceptance.log,test-integrity.txt}`)\n", report.Artifacts.LogsDir)
	return b.String()
}

func agentDescription(report *Report) string {
	if report.Config.MockAgent {
		return fmt.Sprintf("deterministic mock agent (mode=%s; synthetic usage JSON)", report.Config.MockMode)
	}
	desc := "real agent command: `" + report.Config.AgentCmd + "`"
	return desc
}

func formatCI(ci WilsonCI) string {
	return fmt.Sprintf("%.2f-%.2f", ci.Low, ci.High)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
