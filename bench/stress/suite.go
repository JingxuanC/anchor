// Package main implements the Anchor multi-session continuity stress benchmark.
//
// Ordinary single-session coding tasks are saturated: both Anchor and a
// baseline pass them, so they cannot show whether durable session state helps.
// This suite forces continuity to be the only thing that can carry a decision
// across a process boundary:
//
//   - phase 1 tells a fresh agent a randomly chosen hidden convention that the
//     visible tests deliberately do not pin down;
//   - the phase-1 agent (or the offline mock agent) must record the convention
//     itself as an Anchor decision — the harness never seeds decisions;
//   - phase 2 runs in a fresh process and must implement a new function
//     consistently with that convention;
//   - acceptance is the phase-1 visible tests plus a hidden test for the
//     variant that was chosen.
//
// Arms isolate the mechanisms: baseline (no durable state), rules (Anchor
// rules/skill plus the wrap context file, but no handoff packet), packet (a
// handoff packet, but no rules/skill) and full (rules/skill, packet, restore
// ack/verify). See README.md.
package main

import (
	"io"
	"time"
)

const (
	suiteName     = "anchor-multisession-stress"
	suiteVersion  = "1.0.0"
	schemaVersion = 1
)

// Arms.
const (
	armBaseline = "baseline"
	armRules    = "rules"
	armPacket   = "packet"
	armFull     = "full"
)

// Anchor node ids the harness creates for every anchor-arm workspace. The
// harness only creates the goal and the two task nodes; decisions are always
// recorded by the phase-1 agent (or the mock agent), never by the harness.
const (
	goalNodeID   = "goal-stress"
	phase1TaskID = "task-phase1"
	phase2TaskID = "task-phase2"
)

// Mock modes.
const (
	mockModeNormal   = "normal"
	mockModeSabotage = "sabotage"
	mockModeCheater  = "cheater"
)

// Environment variables used to drive the scripted mock agent. They are also
// documented in README.md so a real agent wrapper can follow the same protocol.
const (
	envMockExec        = "STRESS_MOCK_AGENT_EXEC"
	envMockPlan        = "STRESS_MOCK_PLAN"
	envMockMode        = "STRESS_MOCK_MODE"
	envPhase           = "STRESS_PHASE"
	envTask            = "STRESS_TASK"
	envArm             = "STRESS_ARM"
	envVariant         = "STRESS_VARIANT"
	envProject         = "STRESS_PROJECT"
	envPromptFile      = "STRESS_PROMPT_FILE"
	envContextFiles    = "STRESS_CONTEXT_FILES"
	envPacket          = "STRESS_PACKET"
	envRulesFile       = "STRESS_RULES_FILE"
	envAnchorWorkspace = "STRESS_ANCHOR_WORKSPACE"
)

// Config is the resolved configuration of one benchmark run.
type Config struct {
	Trials     int
	Arms       []string
	Tasks      []string
	MockAgent  bool
	MockModes  []string
	AgentCmd   string
	AgentModel string
	AnchorBin  string
	Timeout    time.Duration
	OutDir     string
	JSON       bool
	KeepWork   bool
	MaxUSD     float64
	MaxTokens  int64
	Seed       int64

	// Internal/runtime fields resolved by Run.
	FixturesDir string
	ModuleRoot  string
	MockExec    string
	GoBin       string
	Stdout      io.Writer
}

// Report is the machine-readable benchmark result written to report.json.
type Report struct {
	SchemaVersion  int                       `json:"schema_version"`
	Suite          string                    `json:"suite"`
	SuiteVersion   string                    `json:"suite_version"`
	GeneratedAt    string                    `json:"generated_at"`
	Config         ReportConfig              `json:"config"`
	Metadata       ReportMetadata            `json:"metadata"`
	Tasks          []TaskMeta                `json:"tasks"`
	Arms           []string                  `json:"arms"`
	Trials         []TrialRow                `json:"trials"`
	Aggregates     map[string]*ArmSummary    `json:"aggregates"`
	PerTask        map[string]TaskSummary    `json:"per_task,omitempty"`
	VariantsUsed   map[string]map[string]int `json:"variants_used"`
	Budget         Budget                    `json:"budget"`
	Truncated      bool                      `json:"truncated"`
	BudgetExceeded bool                      `json:"budget_exceeded"`
	BudgetNote     string                    `json:"budget_note,omitempty"`
	Artifacts      ReportArtifacts           `json:"artifacts"`
	Environment    []string                  `json:"environment_notes,omitempty"`
	Limitations    []string                  `json:"limitations,omitempty"`
}

// ReportConfig records exactly how a run was produced.
type ReportConfig struct {
	Trials        int      `json:"trials"`
	Tasks         []string `json:"tasks"`
	Arms          []string `json:"arms"`
	Seed          int64    `json:"seed"`
	MockAgent     bool     `json:"mock_agent"`
	MockModes     []string `json:"mock_modes,omitempty"`
	AgentCmd      string   `json:"agent_cmd,omitempty"`
	AgentModel    string   `json:"agent_model,omitempty"`
	AnchorBin     string   `json:"anchor_bin,omitempty"`
	TimeoutMS     int64    `json:"timeout_ms"`
	OutDir        string   `json:"out_dir"`
	FixturesDir   string   `json:"fixtures_dir"`
	KeepWork      bool     `json:"keep_work"`
	MaxUSD        float64  `json:"max_usd,omitempty"`
	MaxTokens     int64    `json:"max_tokens,omitempty"`
	PromptMode    string   `json:"prompt_mode"`
	AgentProtocol string   `json:"agent_protocol"`
	GoVersion     string   `json:"go_version"`
	AnchorVersion string   `json:"anchor_version"`
}

// ReportMetadata carries environment identity for reproducibility.
type ReportMetadata struct {
	SuiteVersion  string            `json:"suite_version"`
	GitCommit     string            `json:"git_commit,omitempty"`
	ModuleRoot    string            `json:"module_root,omitempty"`
	GoVersion     string            `json:"go_version"`
	AnchorVersion string            `json:"anchor_version"`
	AnchorBin     string            `json:"anchor_bin,omitempty"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	MockAgent     bool              `json:"mock_agent"`
	MockModes     []string          `json:"mock_modes,omitempty"`
	Extra         map[string]string `json:"extra,omitempty"`
}

// TaskMeta pins the exact fixture content a run used.
type TaskMeta struct {
	Name         string   `json:"name"`
	Hash         string   `json:"hash"`
	Package      string   `json:"package"`
	Variants     []string `json:"variants"`
	VisibleTests []string `json:"visible_tests"`
}

// TrialRow is one (task, variant, trial, arm) observation.
type TrialRow struct {
	Task                string            `json:"task"`
	Variant             string            `json:"variant"`
	VariantStatement    string            `json:"variant_statement,omitempty"`
	Trial               int               `json:"trial"`
	Arm                 string            `json:"arm"`
	MockMode            string            `json:"mock_mode,omitempty"`
	Phase1Pass          bool              `json:"phase1_pass"`
	Phase2Pass          bool              `json:"phase2_pass"`
	Phase1VisiblePass   bool              `json:"phase1_visible_pass"`
	DecisionsRecorded   bool              `json:"decisions_recorded"`
	DecisionNodeIDs     []string          `json:"decision_node_ids,omitempty"`
	PacketDecisions     int               `json:"packet_decisions"`
	VariantConveyed     bool              `json:"variant_conveyed"`
	VariantLeakedInRepo bool              `json:"variant_leaked_in_repo"`
	TestsModified       bool              `json:"tests_modified"`
	Phase1TestsModified bool              `json:"phase1_tests_modified"`
	Phase2TestsModified bool              `json:"phase2_tests_modified"`
	HandoffPrepared     bool              `json:"handoff_prepared"`
	HandoffPreparedBy   string            `json:"handoff_prepared_by,omitempty"`
	HandoffRefreshed    bool              `json:"handoff_refreshed"`
	HandoffAcked        bool              `json:"handoff_acked"`
	HandoffVerified     bool              `json:"handoff_verified"`
	WrapFallback        bool              `json:"wrap_fallback"`
	AgentExitPhase1     int               `json:"agent_exit_phase1"`
	AgentExitPhase2     int               `json:"agent_exit_phase2"`
	Phase1MS            int64             `json:"phase1_ms"`
	Phase2MS            int64             `json:"phase2_ms"`
	WallMS              int64             `json:"wall_ms"`
	Tokens              int64             `json:"tokens"`
	CostUSD             float64           `json:"cost_usd"`
	TrialDir            string            `json:"trial_dir,omitempty"`
	Artifacts           map[string]string `json:"artifacts,omitempty"`
	Error               string            `json:"error,omitempty"`
	Notes               []string          `json:"notes,omitempty"`
}

// WilsonCI is a 95% Wilson score interval for a pass rate.
type WilsonCI struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// ArmSummary aggregates one arm across every task and trial.
type ArmSummary struct {
	Arm               string         `json:"arm"`
	Trials            int            `json:"trials"`
	Phase1Pass        int            `json:"phase1_pass"`
	Phase2Pass        int            `json:"phase2_pass"`
	Phase1PassAt1     float64        `json:"phase1_pass_at_1"`
	Phase2PassAt1     float64        `json:"phase2_pass_at_1"`
	Phase1CI          WilsonCI       `json:"phase1_ci95"`
	Phase2CI          WilsonCI       `json:"phase2_ci95"`
	Phase2PassK       float64        `json:"phase2_pass_at_k"`
	PassK             int            `json:"k"`
	MeanTokens        float64        `json:"mean_tokens"`
	MeanCostUSD       float64        `json:"mean_cost_usd"`
	DecisionsRecorded int            `json:"decisions_recorded"`
	PacketDecisions   int            `json:"packet_decisions"`
	VariantConveyed   int            `json:"variant_conveyed"`
	VariantLeaks      int            `json:"variant_leaked_in_repo"`
	TestsModified     int            `json:"tests_modified"`
	HandoffVerified   int            `json:"handoff_verified"`
	VariantsUsed      map[string]int `json:"variants_used,omitempty"`
}

// TaskSummary is a per-task, per-arm pass count used to show discrimination.
type TaskSummary struct {
	Task string                   `json:"task"`
	Arms map[string]*TaskArmStats `json:"arms"`
}

// TaskArmStats is one arm's result for one task.
type TaskArmStats struct {
	Trials        int     `json:"trials"`
	Phase1Pass    int     `json:"phase1_pass"`
	Phase2Pass    int     `json:"phase2_pass"`
	Phase2PassAt1 float64 `json:"phase2_pass_at_1"`
}

// Budget records the caps that were in force and what was actually spent.
type Budget struct {
	MaxUSD        float64 `json:"max_usd,omitempty"`
	MaxTokens     int64   `json:"max_tokens,omitempty"`
	TokensUsed    int64   `json:"tokens_used"`
	CostUSD       float64 `json:"cost_usd"`
	TrialsPlanned int     `json:"trials_planned"`
	TrialsRun     int     `json:"trials_run"`
}

// ReportArtifacts points at the files a run wrote.
type ReportArtifacts struct {
	Root       string `json:"root"`
	ReportJSON string `json:"report_json"`
	ReportMD   string `json:"report_md"`
	TrialDirs  int    `json:"trial_dirs"`
}

// isAnchorArm reports whether the arm runs the phase-1 session inside an
// Anchor workspace.
func isAnchorArm(arm string) bool { return arm != armBaseline }

// hasRules reports whether the arm gives the phase-2 session Anchor's
// rules/skill text.
func hasRules(arm string) bool { return arm == armRules || arm == armFull }

// hasPacket reports whether the arm prepares and hands over a handoff packet.
func hasPacket(arm string) bool { return arm == armPacket || arm == armFull }

// armRank orders arms from least to most context, used for the ablation check
// reported in the CLI summary and the tests.
func armRank(arm string) int {
	switch arm {
	case armBaseline:
		return 0
	case armRules:
		return 1
	case armPacket:
		return 2
	case armFull:
		return 3
	}
	return -1
}

// limitations is a fixed, honest list included in every report.
func limitations() []string {
	return []string{
		"Local isolation only: phases run as separate local processes, not across machines or weeks.",
		"Mock mode is a harness self-test; it proves the mechanics and the acceptance tests discriminate, not that any real agent records decisions.",
		"Real-agent compliance (decisions_recorded, packet_decisions, variant_conveyed) is measured and reported, never assumed.",
		"The baseline phase-2 session still sees the repository and git history; a real phase-1 agent that writes the convention into tracked files would leak it into every arm. The mock agent never does this.",
		"The rules arm keeps the live .anchor workspace visible, so a real agent that queries `anchor tree` may recover the decision without a packet; the mock only reads the context it is given, so the mock's rules result is a lower bound.",
		"Budget caps (--max-usd/--max-tokens) use agent-reported usage; an agent that reports no usage cannot trip them, and the reported tokens/cost only cover what was reported.",
		"Three small authored tasks and a handful of trials are enough to show discrimination, not to estimate absolute agent capability.",
	}
}
