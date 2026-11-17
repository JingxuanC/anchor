package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// runState is the resolved, immutable-ish context of one benchmark run.
type runState struct {
	cfg           *Config
	outDir        string
	fixturesDir   string
	moduleRoot    string
	goBin         string
	anchorBin     string
	anchorVersion string
	skillText     string
	rng           *rand.Rand
	seed          int64
}

// Run executes the whole benchmark and returns (and writes) its report.
func Run(ctx context.Context, cfg *Config) (*Report, error) {
	st, err := newRunState(ctx, cfg)
	if err != nil {
		return nil, err
	}
	fixtures, err := loadFixtures(st.fixturesDir, cfg.Tasks)
	if err != nil {
		return nil, err
	}
	needAnchor := false
	for _, arm := range cfg.Arms {
		if isAnchorArm(arm) {
			needAnchor = true
		}
	}
	if needAnchor {
		if st.anchorBin, err = st.resolveAnchor(ctx); err != nil {
			return nil, err
		}
		st.anchorVersion = st.anchorVersionOf(ctx)
		st.skillText = st.anchorSkill(ctx)
	} else {
		st.anchorVersion = "not_used"
	}
	assignments := map[string][]*LoaderVariant{}
	variantCounts := map[string]map[string]int{}
	for _, fx := range fixtures {
		assignments[fx.Name] = assignVariants(st.rng, fx, cfg.Trials)
		variantCounts[fx.Name] = map[string]int{}
	}

	report := &Report{
		SchemaVersion: schemaVersion,
		Suite:         suiteName,
		SuiteVersion:  suiteVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Arms:          append([]string{}, cfg.Arms...),
		Aggregates:    map[string]*ArmSummary{},
		PerTask:       map[string]TaskSummary{},
		VariantsUsed:  variantCounts,
		Environment: []string{
			"go: " + runtime.Version(),
			"os: " + runtime.GOOS + "/" + runtime.GOARCH,
			"mock_agent: " + fmt.Sprintf("%v", cfg.MockAgent),
			"seed: " + fmt.Sprintf("%d", st.seed),
		},
		Limitations: limitations(),
	}
	report.Config = ReportConfig{
		Trials:        cfg.Trials,
		Tasks:         fixtureNames(fixtures),
		Arms:          append([]string{}, cfg.Arms...),
		Seed:          st.seed,
		MockAgent:     cfg.MockAgent,
		MockModes:     cfg.MockModes,
		AgentCmd:      cfg.AgentCmd,
		AgentModel:    cfg.AgentModel,
		AnchorBin:     st.anchorBin,
		TimeoutMS:     cfg.Timeout.Milliseconds(),
		OutDir:        st.outDir,
		FixturesDir:   st.fixturesDir,
		KeepWork:      cfg.KeepWork,
		MaxUSD:        cfg.MaxUSD,
		MaxTokens:     cfg.MaxTokens,
		PromptMode:    "phase1+phase2 prompt files; prompt appended to agent argv",
		AgentProtocol: "env STRESS_* / ANCHOR_* documented in bench/stress/README.md",
		GoVersion:     runtime.Version(),
		AnchorVersion: st.anchorVersion,
	}
	gitCommit := ""
	if st.moduleRoot != "" {
		if out, err := exec.Command("git", "-C", st.moduleRoot, "rev-parse", "HEAD").Output(); err == nil {
			gitCommit = strings.TrimSpace(string(out))
		}
	}
	report.Metadata = ReportMetadata{
		SuiteVersion:  suiteVersion,
		GitCommit:     gitCommit,
		ModuleRoot:    st.moduleRoot,
		GoVersion:     runtime.Version(),
		AnchorVersion: st.anchorVersion,
		AnchorBin:     st.anchorBin,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		MockAgent:     cfg.MockAgent,
		MockModes:     cfg.MockModes,
	}
	for _, fx := range fixtures {
		report.Tasks = append(report.Tasks, TaskMeta{
			Name:         fx.Name,
			Hash:         fx.Hash,
			Package:      fx.Conv.Package,
			Variants:     variantIDs(fx),
			VisibleTests: fx.Conv.VisibleTests,
		})
	}

	planned := len(fixtures) * cfg.Trials * len(cfg.Arms)
	var totalTokens int64
	var totalCost float64
	stoppedForBudget := false
	budgetHit := func() bool {
		if cfg.MaxTokens > 0 && totalTokens >= cfg.MaxTokens {
			return true
		}
		if cfg.MaxUSD > 0 && totalCost >= cfg.MaxUSD {
			return true
		}
		return false
	}

loop:
	for _, fx := range fixtures {
		for trial := 0; trial < cfg.Trials; trial++ {
			if budgetHit() {
				stoppedForBudget = true
				break loop
			}
			variant := assignments[fx.Name][trial]
			variantCounts[fx.Name][variant.ID]++
			for _, arm := range cfg.Arms {
				if budgetHit() {
					stoppedForBudget = true
					break loop
				}
				mode := cfg.MockModes[trial%len(cfg.MockModes)]
				row := st.runTrial(ctx, fx, variant, trial, arm, mode)
				report.Trials = append(report.Trials, row)
				totalTokens += row.Tokens
				totalCost += row.CostUSD
				if st.cfg.Stdout != nil {
					fmt.Fprintf(st.cfg.Stdout, "[%s t%d %s %s] phase1=%v phase2=%v decisions=%v packet_decisions=%d conveyed=%v tests_modified=%v\n",
						fx.Name, trial, arm, variant.ID, row.Phase1Pass, row.Phase2Pass,
						row.DecisionsRecorded, row.PacketDecisions, row.VariantConveyed, row.TestsModified)
				}
			}
		}
	}

	report.Aggregates = summarize(report.Trials, cfg.Arms, cfg.Trials)
	report.PerTask = summarizePerTask(report.Trials)
	report.Budget = Budget{
		MaxUSD:        cfg.MaxUSD,
		MaxTokens:     cfg.MaxTokens,
		TokensUsed:    totalTokens,
		CostUSD:       roundUSD(totalCost),
		TrialsPlanned: planned,
		TrialsRun:     len(report.Trials),
	}
	report.Truncated = len(report.Trials) < planned
	report.BudgetExceeded = stoppedForBudget
	if report.Truncated {
		report.BudgetNote = fmt.Sprintf("stopped after %d of %d planned trials", len(report.Trials), planned)
		if stoppedForBudget {
			report.BudgetNote += fmt.Sprintf(" (budget cap: max_tokens=%d max_usd=%g)", cfg.MaxTokens, cfg.MaxUSD)
		}
	}
	if err := writeReport(st.outDir, report); err != nil {
		return nil, err
	}
	return report, nil
}

func newRunState(ctx context.Context, cfg *Config) (*runState, error) {
	if len(cfg.Arms) == 0 {
		return nil, fmt.Errorf("no arms selected")
	}
	if cfg.Trials < 1 {
		return nil, fmt.Errorf("trials must be at least 1")
	}
	if len(cfg.MockModes) == 0 {
		cfg.MockModes = []string{mockModeNormal}
	}
	if cfg.GoBin == "" {
		cfg.GoBin = "go"
	}
	if _, err := exec.LookPath(cfg.GoBin); err != nil {
		return nil, fmt.Errorf("go toolchain %q not found: %w", cfg.GoBin, err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is required for handoff code state: %w", err)
	}
	st := &runState{cfg: cfg}
	root, err := findModuleRoot()
	if err != nil {
		return nil, err
	}
	st.moduleRoot = root
	st.fixturesDir = cfg.FixturesDir
	if st.fixturesDir == "" {
		if root == "" {
			return nil, fmt.Errorf("cannot locate the module; pass --fixtures")
		}
		st.fixturesDir = filepath.Join(root, "bench", "fixtures", "stress")
	}
	if abs, err := filepath.Abs(st.fixturesDir); err == nil {
		st.fixturesDir = abs
	}
	if _, err := os.Stat(st.fixturesDir); err != nil {
		return nil, fmt.Errorf("fixtures dir %s: %w", st.fixturesDir, err)
	}
	st.outDir = cfg.OutDir
	if st.outDir == "" {
		st.outDir = defaultOutDir(root)
		if st.outDir == "" {
			st.outDir = filepath.Join(os.TempDir(), "anchor-stress-out")
		}
	}
	if abs, err := filepath.Abs(st.outDir); err == nil {
		st.outDir = abs
	}
	if err := os.MkdirAll(st.outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	st.goBin = cfg.GoBin
	st.seed = cfg.Seed
	if st.seed == 0 {
		st.seed = time.Now().UnixNano()
	}
	st.rng = rand.New(rand.NewSource(st.seed))
	if cfg.MockAgent {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve mock agent executable: %w", err)
		}
		cfg.MockExec = exe
	} else {
		if _, err := splitCommand(cfg.AgentCmd); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// assignVariants returns one variant per trial: a shuffled rotation, so every
// variant is used about equally while the per-trial choice stays random under
// the seed. This makes the suite statistically meaningful with few trials.
func assignVariants(rng *rand.Rand, fx *Fixture, trials int) []*LoaderVariant {
	n := len(fx.Variants)
	order := make([]*LoaderVariant, n)
	for i := range fx.Variants {
		order[i] = &fx.Variants[i]
	}
	rng.Shuffle(n, func(i, j int) { order[i], order[j] = order[j], order[i] })
	out := make([]*LoaderVariant, trials)
	for i := 0; i < trials; i++ {
		out[i] = order[i%n]
	}
	return out
}

func fixtureNames(fixtures []*Fixture) []string {
	out := make([]string, 0, len(fixtures))
	for _, fx := range fixtures {
		out = append(out, fx.Name)
	}
	return out
}

// findModuleRoot walks up from the working directory looking for this
// repository's go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(candidate); err == nil {
			if strings.Contains(string(b), "module github.com/JingxuanC/anchor") {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// resolveAnchor returns an anchor binary: an explicit one, a fresh build from
// this checkout, or one on PATH (in that order).
func (st *runState) resolveAnchor(ctx context.Context) (string, error) {
	if st.cfg.AnchorBin != "" {
		abs, err := filepath.Abs(st.cfg.AnchorBin)
		if err != nil {
			return "", err
		}
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			return "", fmt.Errorf("anchor binary %s is not executable", abs)
		}
		return abs, nil
	}
	if st.moduleRoot != "" {
		out := filepath.Join(st.outDir, "bin", "anchor")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		res := runCommand(ctx, 3*time.Minute, st.moduleRoot, baseEnv(nil), nil, st.goBin, "build", "-o", out, "./cmd/anchor")
		if res.ExitCode == 0 {
			return out, nil
		}
	}
	if p, err := exec.LookPath("anchor"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no anchor binary: pass --anchor-bin or run from this checkout")
}

func (st *runState) anchorVersionOf(ctx context.Context) string {
	res := runCommand(ctx, st.cfg.Timeout, st.moduleRoot, baseEnv(nil), nil, st.anchorBin, "version")
	out := strings.TrimSpace(res.Output)
	if res.ExitCode != 0 || out == "" {
		return "unknown"
	}
	return firstLine(out)
}

func (st *runState) anchorSkill(ctx context.Context) string {
	res := runCommand(ctx, st.cfg.Timeout, st.moduleRoot, baseEnv(nil), nil, st.anchorBin, "skill", "print")
	if res.ExitCode != 0 {
		return ""
	}
	return res.Output
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// One trial

func (st *runState) runTrial(ctx context.Context, fx *Fixture, variant *LoaderVariant, trial int, arm, mode string) TrialRow {
	row := TrialRow{
		Task:              fx.Name,
		Variant:           variant.ID,
		VariantStatement:  variant.Statement,
		Trial:             trial,
		Arm:               arm,
		MockMode:          modeIfMock(st.cfg.MockAgent, mode),
		HandoffPreparedBy: "",
		Artifacts:         map[string]string{},
	}
	start := time.Now()
	// NOTE: the trial directory must not contain the variant id. The phase-2
	// context includes paths (wrap context, packet) and a variant id in a path
	// would count as "conveyed" without any convention actually being carried.
	dir := uniqueDir(filepath.Join(st.outDir, "trials"), fmt.Sprintf("%s-t%02d-%s", fx.Name, trial, arm))
	project := filepath.Join(dir, "project")
	row.TrialDir = relPath(st.outDir, dir)
	trialLog := func(format string, args ...any) {
		row.Notes = append(row.Notes, fmt.Sprintf(format, args...))
	}
	fail := func(err error) TrialRow {
		if err != nil {
			if row.Error != "" {
				row.Error += "; "
			}
			row.Error += err.Error()
		}
		row.WallMS = time.Since(start).Milliseconds()
		if !st.cfg.KeepWork {
			_ = os.RemoveAll(project)
		}
		return row
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(fmt.Errorf("create trial dir: %w", err))
	}
	if err := copyTree(fx.RepoDir, project); err != nil {
		return fail(fmt.Errorf("copy fixture: %w", err))
	}
	initialHead, err := gitInitCommit(project)
	if err != nil {
		return fail(fmt.Errorf("git init: %w", err))
	}

	// ---- phase 1 prompt + mock plan
	phase1Prompt := promptPhase1(fx, variant, arm)
	if _, err := writeArtifact(st, dir, "prompt_phase1.md", phase1Prompt); err != nil {
		return fail(err)
	}
	row.Artifacts["prompt_phase1"] = relPath(st.outDir, filepath.Join(dir, "prompt_phase1.md"))

	stateDir := ""
	if isAnchorArm(arm) {
		if err := st.setupAnchor(ctx, project); err != nil {
			trialLog("anchor setup failed: %v", err)
			row.Error = appendErr(row.Error, fmt.Errorf("anchor setup: %w", err))
		} else {
			stateDir = filepath.Join(project, ".anchor")
		}
	}

	phase1Plan := &mockPlan{
		Phase:           1,
		Task:            fx.Name,
		Arm:             arm,
		MockMode:        mode,
		Project:         project,
		PromptFile:      filepath.Join(dir, "prompt_phase1.md"),
		Variant:         variant.ID,
		VariantPhrase:   variant.Phrase,
		PackageName:     fx.Conv.Package,
		Phase1File:      fx.Conv.Phase1File,
		Phase1Code:      fx.Phase1Mock,
		Phase1CodeBad:   fixturePhase1Stub(fx),
		VisibleTests:    fx.Conv.VisibleTests,
		CommitMessage:   "phase 1: implement " + fx.Conv.Phase1File,
		Commit:          true,
		AnchorWorkspace: stateDir,
		AnchorBin:       st.anchorBin,
		Phase2File:      fx.Conv.Phase2File,
		Phase2Correct:   variant.MockPhase2,
		Phase2Wrong:     fx.Phase2MockWrong,
		Usage:           mockUsage{Input: 1200, Output: 300, CostUSD: 0.0004},
	}
	if isAnchorArm(arm) && stateDir != "" {
		phase1Plan.Decisions = []mockDecision{{
			ID:        variantDecisionID(fx, variant),
			Parent:    phase1TaskID,
			Title:     fmt.Sprintf("%s: %s", fx.Conv.DecisionTitle, variant.ID),
			Decision:  variant.Statement,
			Rationale: "chosen in phase 1; the visible tests do not pin it down",
		}}
	}
	plan1Path := filepath.Join(dir, "mock_plan_phase1.json")
	if st.cfg.MockAgent {
		if err := writeMockPlan(plan1Path, phase1Plan); err != nil {
			return fail(err)
		}
		row.Artifacts["mock_plan_phase1"] = relPath(st.outDir, plan1Path)
	}

	// ---- phase 1 agent
	phase1Start := time.Now()
	agentArgv1, err := agentArgv(st.cfg, phase1Prompt, phase1Plan.PromptFile)
	if err != nil {
		return fail(err)
	}
	env1 := phaseEnvSpec{
		phase: 1, task: fx.Name, arm: arm, variant: variant.ID, project: project,
		promptFile: phase1Plan.PromptFile,
		planFile:   planFileIfMock(st.cfg.MockAgent, plan1Path), mode: mode,
		anchorBin: st.anchorBin, anchorWorkspace: stateDir,
	}.env()
	var wrapArgv []string
	if isAnchorArm(arm) && stateDir != "" {
		wrapArgv = []string{st.anchorBin, "wrap", "--dir", project, "--node", phase1TaskID, "--repo", project}
		if hasPacket(arm) {
			wrapArgv = append(wrapArgv, "--prepare-handoff")
		}
		wrapArgv = append(wrapArgv, "--")
		wrapArgv = append(wrapArgv, agentArgv1...)
	} else {
		wrapArgv = agentArgv1
	}
	phase1Res := runLogged(ctx, st.cfg.Timeout, project, env1, filepath.Join(dir, "log_phase1_agent.log"), wrapArgv...)
	row.AgentExitPhase1 = phase1Res.ExitCode
	if phase1Res.TimedOut {
		trialLog("phase 1 agent timed out")
	}
	row.Artifacts["log_phase1_agent"] = relPath(st.outDir, filepath.Join(dir, "log_phase1_agent.log"))
	usage1 := parseTokenUsage(phase1Res.Output)
	row.Tokens += usage1.total()
	row.CostUSD += usage1.CostUSD

	wrapContextPath, wrapContext := st.latestWrapContext(stateDir)
	if wrapContextPath != "" {
		row.Artifacts["wrap_context"] = relPath(st.outDir, filepath.Join(dir, "wrap_context.md"))
		if _, err := writeArtifact(st, dir, "wrap_context.md", wrapContext); err != nil {
			trialLog("write wrap context: %v", err)
		}
	}

	// ---- phase 1 acceptance (canonical visible tests only)
	if modified, restored, err := restoreVisibleTests(project, fx); err != nil {
		trialLog("restore visible tests: %v", err)
	} else if modified {
		row.Phase1TestsModified = true
		row.TestsModified = true
		trialLog("phase 1 tampered with visible tests (%s); canonical tests restored", strings.Join(restored, ","))
	}
	phase1Accept := st.runGoTest(ctx, project, "accept_phase1.log")
	row.Phase1Pass = phase1Accept.ExitCode == 0
	row.VariantLeakedInRepo = variantLeakedInRepo(project, variant)
	if row.VariantLeakedInRepo {
		trialLog("phase-1 agent left the convention recoverable in the repository (phase 2 can leak it)")
	}
	row.Phase1VisiblePass = row.Phase1Pass
	row.Artifacts["accept_phase1"] = relPath(st.outDir, filepath.Join(dir, "accept_phase1.log"))

	if _, head, err := gitCommitIfDirty(project, "bench: phase 1 snapshot"); err != nil {
		trialLog("commit phase 1: %v", err)
	} else if head != "" {
		trialLog("phase 1 snapshot committed: %s", head)
	}
	row.Phase1MS = time.Since(phase1Start).Milliseconds()

	// ---- anchor post-phase-1: compliance, handoff, restore
	var packetPath, packetJSON string
	var packetDecisions int
	if stateDir != "" {
		decisionIDs, recorded := st.checkDecisions(ctx, project, variant)
		row.DecisionsRecorded = recorded
		row.DecisionNodeIDs = decisionIDs
		if !recorded {
			trialLog("no decision node carrying variant %q was found; the phase-1 agent did not record the convention", variant.ID)
		}
		if hasPacket(arm) {
			var prepErr error
			packetPath, packetJSON, packetDecisions, prepErr = st.ensureHandoff(ctx, project, stateDir, variant, &row)
			if prepErr != nil {
				trialLog("handoff prepare: %v", prepErr)
				row.Error = appendErr(row.Error, fmt.Errorf("handoff prepare: %w", prepErr))
			} else {
				row.HandoffPrepared = true
				row.PacketDecisions = packetDecisions
				row.Artifacts["packet"] = relPath(st.outDir, filepath.Join(dir, "packet.json"))
				if _, err := writeArtifact(st, dir, "packet.json", packetJSON); err != nil {
					trialLog("write packet artifact: %v", err)
				}
			}
			if arm == armFull && packetPath != "" {
				acked, verified := st.restoreHandoff(ctx, project, dir, packetPath, &row)
				row.HandoffAcked = acked
				row.HandoffVerified = verified
				if !verified {
					trialLog("full arm: handoff restore --ack/--verify did not both succeed")
				}
			}
		}
	}

	// ---- phase 2 prompt + mock plan
	phase2Prompt := buildPhase2Prompt(st, fx, variant, arm, wrapContext, wrapContextPath, packetPath, packetJSON)
	phase2PromptPath := filepath.Join(dir, "prompt_phase2.md")
	if _, err := writeArtifact(st, dir, "prompt_phase2.md", phase2Prompt); err != nil {
		return fail(err)
	}
	row.Artifacts["prompt_phase2"] = relPath(st.outDir, phase2PromptPath)

	var contextFiles []string
	if hasRules(arm) && wrapContextPath != "" {
		contextFiles = append(contextFiles, wrapContextPath)
	}
	row.VariantConveyed = variantInText(variant, phase2Prompt)
	if !row.VariantConveyed && packetJSON != "" {
		row.VariantConveyed = variantInText(variant, packetJSON)
	}

	phase2Plan := &mockPlan{
		Phase:           2,
		Task:            fx.Name,
		Arm:             arm,
		MockMode:        mode,
		Project:         project,
		PromptFile:      phase2PromptPath,
		Variant:         variant.ID,
		VariantPhrase:   variant.Phrase,
		PackageName:     fx.Conv.Package,
		Phase1File:      fx.Conv.Phase1File,
		Phase2File:      fx.Conv.Phase2File,
		Phase2Correct:   variant.MockPhase2,
		Phase2Wrong:     fx.Phase2MockWrong,
		VisibleTests:    fx.Conv.VisibleTests,
		ContextFiles:    contextFiles,
		Packet:          packetPath,
		AnchorWorkspace: stateDir,
		AnchorBin:       st.anchorBin,
		Usage:           mockUsage{Input: 900, Output: 400, CostUSD: 0.0003},
	}
	plan2Path := filepath.Join(dir, "mock_plan_phase2.json")
	if st.cfg.MockAgent {
		if err := writeMockPlan(plan2Path, phase2Plan); err != nil {
			return fail(err)
		}
		row.Artifacts["mock_plan_phase2"] = relPath(st.outDir, plan2Path)
	}

	// ---- phase 2 agent (fresh process, no wrap)
	phase2Start := time.Now()
	agentArgv2, err := agentArgv(st.cfg, phase2Prompt, phase2PromptPath)
	if err != nil {
		return fail(err)
	}
	env2 := phaseEnvSpec{
		phase: 2, task: fx.Name, arm: arm, project: project,
		promptFile: phase2PromptPath,
		planFile:   planFileIfMock(st.cfg.MockAgent, plan2Path), mode: mode,
		anchorBin: st.anchorBin, anchorWorkspace: stateDir,
		contextFiles: contextFiles, packet: packetPath,
	}.env()
	phase2Res := runLogged(ctx, st.cfg.Timeout, project, env2, filepath.Join(dir, "log_phase2_agent.log"), agentArgv2...)
	row.AgentExitPhase2 = phase2Res.ExitCode
	if phase2Res.TimedOut {
		trialLog("phase 2 agent timed out")
	}
	row.Artifacts["log_phase2_agent"] = relPath(st.outDir, filepath.Join(dir, "log_phase2_agent.log"))
	usage2 := parseTokenUsage(phase2Res.Output)
	row.Tokens += usage2.total()
	row.CostUSD += usage2.CostUSD

	if modified, restored, err := restoreVisibleTests(project, fx); err != nil {
		trialLog("restore visible tests phase 2: %v", err)
	} else if modified {
		row.Phase2TestsModified = true
		row.TestsModified = true
		trialLog("phase 2 tampered with visible tests (%s); canonical tests restored", strings.Join(restored, ","))
	}
	if _, _, err := gitCommitIfDirty(project, "bench: phase 2 snapshot"); err != nil {
		trialLog("commit phase 2: %v", err)
	}
	if patch, err := gitDiffFrom(project, initialHead); err != nil {
		trialLog("git diff: %v", err)
	} else if strings.TrimSpace(patch) != "" {
		if _, err := writeArtifact(st, dir, "patch.diff", patch); err != nil {
			trialLog("write patch: %v", err)
		} else {
			row.Artifacts["patch"] = relPath(st.outDir, filepath.Join(dir, "patch.diff"))
		}
	}

	// ---- phase 2 acceptance: visible tests + hidden test for the variant
	hiddenName := "zz_hidden_" + sanitize(variant.ID) + "_test.go"
	if err := os.WriteFile(filepath.Join(project, hiddenName), []byte(variant.HiddenTest), 0o644); err != nil {
		return fail(fmt.Errorf("copy hidden test: %w", err))
	}
	if _, err := writeArtifact(st, dir, hiddenName, variant.HiddenTest); err != nil {
		trialLog("write hidden test artifact: %v", err)
	} else {
		row.Artifacts["hidden_test"] = relPath(st.outDir, filepath.Join(dir, hiddenName))
	}
	phase2Accept := st.runGoTest(ctx, project, "accept_phase2.log")
	row.Phase2Pass = phase2Accept.ExitCode == 0
	row.Artifacts["accept_phase2"] = relPath(st.outDir, filepath.Join(dir, "accept_phase2.log"))
	row.Phase2MS = time.Since(phase2Start).Milliseconds()

	row.WallMS = time.Since(start).Milliseconds()
	if !st.cfg.KeepWork {
		_ = os.RemoveAll(project)
	}
	return row
}

func modeIfMock(mock bool, mode string) string {
	if !mock {
		return ""
	}
	return mode
}

func planFileIfMock(mock bool, path string) string {
	if !mock {
		return ""
	}
	return path
}

func appendErr(base string, err error) string {
	if err == nil {
		return base
	}
	if base == "" {
		return err.Error()
	}
	return base + "; " + err.Error()
}

// fixturePhase1Stub reconstructs the repository's original stub file content
// (the sabotage mock writes it after breaking the visible tests).
func fixturePhase1Stub(fx *Fixture) string {
	b, err := os.ReadFile(filepath.Join(fx.RepoDir, fx.Conv.Phase1File))
	if err != nil {
		return ""
	}
	return string(b)
}

// runLogged runs argv and streams combined output into logName inside dir.
func runLogged(ctx context.Context, timeout time.Duration, workDir string, env []string, logPath string, argv ...string) commandResult {
	f, err := os.Create(logPath)
	if err != nil {
		return runCommand(ctx, timeout, workDir, env, nil, argv...)
	}
	defer f.Close()
	res := runCommand(ctx, timeout, workDir, env, f, argv...)
	if res.Err != nil {
		fmt.Fprintf(f, "\ncommand error: %v\n", res.Err)
	}
	return res
}

func writeArtifact(st *runState, dir, name, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

func relPath(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

// uniqueDir returns base/name, adding a numeric suffix if it already exists.
func uniqueDir(base, name string) string {
	dir := filepath.Join(base, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return dir
	}
	for i := 2; ; i++ {
		candidate := filepath.Join(base, fmt.Sprintf("%s-r%02d", name, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Phase-2 prompt and context

// buildPhase2Prompt renders the generic phase-2 prompt plus the arm-specific
// session context. The baseline arm gets only a generic "phase 1 finished,
// continue" summary; no arm receives the convention unless it is carried by the
// durable context the arm provides.
func buildPhase2Prompt(st *runState, fx *Fixture, variant *LoaderVariant, arm, wrapContext, wrapContextPath, packetPath, packetJSON string) string {
	var b strings.Builder
	b.WriteString(fx.Phase2Prompt)
	b.WriteString("\n\n## Session context\n\n")
	switch arm {
	case armBaseline:
		b.WriteString("Previous session summary: phase 1 was implemented and committed. " +
			"The previous session left no durable notes and no handoff, so no project convention was carried over. Continue with the work above.\n")
	default:
		if hasRules(arm) {
			b.WriteString("### Anchor rules/skill provided to this session\n\n")
			if strings.TrimSpace(st.skillText) != "" {
				b.WriteString(st.skillText)
				b.WriteString("\n")
			} else {
				b.WriteString("(Anchor's skill text could not be loaded in this run.)\n")
			}
			if wrapContext != "" {
				fmt.Fprintf(&b, "\n### Wrap context file (generated when phase 1 started)\n\nPath: `%s`\n\n```markdown\n%s\n```\n", wrapContextPath, wrapContext)
			}
		}
		if hasPacket(arm) && packetJSON != "" {
			fmt.Fprintf(&b, "\n### Handoff packet prepared by phase 1\n\nPath: `%s`\n\n```json\n%s\n```\n", packetPath, packetJSON)
			if arm == armFull {
				b.WriteString("\nThe successor ack and verification for this packet were already recorded before this process started.\n")
			}
		}
	}
	b.WriteString("\nRecover the phase-1 convention from the context above if it is present. If it is not present, use your best guess.\n")
	return b.String()
}

// variantInText reports whether text carries the variant id or its distinctive
// statement phrase.
func variantInText(v *LoaderVariant, text string) bool {
	if v == nil || text == "" {
		return false
	}
	if v.ID != "" && strings.Contains(text, v.ID) {
		return true
	}
	if v.Phrase != "" && strings.Contains(text, v.Phrase) {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Anchor helpers

func (st *runState) anchor(ctx context.Context, workDir string, args ...string) commandResult {
	return runCommand(ctx, st.cfg.Timeout, workDir, baseEnv(nil), nil, append([]string{st.anchorBin}, args...)...)
}

func (st *runState) setupAnchor(ctx context.Context, project string) error {
	res := st.anchor(ctx, project, "init", "--attach", "--dir", project, "--agents", "none")
	if res.ExitCode != 0 {
		return fmt.Errorf("anchor init: %s", strings.TrimSpace(res.Output))
	}
	nodes := [][]string{
		{"node", "create", "--dir", project, "--id", goalNodeID, "--type", "goal",
			"--title", "multi-session continuity stress task",
			"--north-star", "finish phase 2 consistently with the phase-1 convention",
			"--must", "the phase-1 agent records its own decisions",
			"--success", "visible tests plus the hidden variant test pass"},
		{"node", "create", "--dir", project, "--id", phase1TaskID, "--type", "task",
			"--parent", goalNodeID, "--title", "phase 1: implement the visible API and record the convention"},
		{"node", "create", "--dir", project, "--id", phase2TaskID, "--type", "task",
			"--parent", goalNodeID, "--title", "phase 2: continue in a fresh session"},
	}
	for _, args := range nodes {
		res := st.anchor(ctx, project, args...)
		if res.ExitCode != 0 {
			return fmt.Errorf("%s: %s", strings.Join(args[:2], " "), strings.TrimSpace(res.Output))
		}
	}
	return nil
}

func (st *runState) latestWrapContext(stateDir string) (string, string) {
	if stateDir == "" {
		return "", ""
	}
	matches, err := filepath.Glob(filepath.Join(stateDir, "local", "wrap", "*.context.md"))
	if err != nil || len(matches) == 0 {
		return "", ""
	}
	sort.Slice(matches, func(i, j int) bool {
		si, _ := os.Stat(matches[i])
		sj, _ := os.Stat(matches[j])
		if si == nil || sj == nil {
			return matches[i] < matches[j]
		}
		return si.ModTime().After(sj.ModTime())
	})
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return "", ""
	}
	return matches[0], string(b)
}

// checkDecisions inspects the Anchor tree (via the CLI) for a decision node that
// carries the variant. The harness never creates decision nodes itself.
func (st *runState) checkDecisions(ctx context.Context, project string, variant *LoaderVariant) ([]string, bool) {
	res := st.anchor(ctx, project, "tree", "--dir", project, "--json")
	if res.ExitCode != 0 {
		return nil, false
	}
	var tree struct {
		Nodes []struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Title     string `json:"title"`
			Decision  string `json:"decision"`
			Rationale string `json:"rationale"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(res.Output), &tree); err != nil {
		return nil, false
	}
	var ids []string
	for _, n := range tree.Nodes {
		if n.Type != "decision" {
			continue
		}
		text := n.ID + "\n" + n.Title + "\n" + n.Decision + "\n" + n.Rationale
		if variantInText(variant, text) {
			ids = append(ids, n.ID)
		}
	}
	return ids, len(ids) > 0
}

// packetView is the subset of the handoff packet the harness needs.
type packetView struct {
	HandoffID      string `json:"handoff_id"`
	ContentHash    string `json:"content_hash"`
	RecoveryStatus string `json:"recovery_status"`
	Decisions      []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Decision  string `json:"decision"`
		Rationale string `json:"rationale"`
	} `json:"decisions"`
	Tree struct {
		Nodes []struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Title     string `json:"title"`
			Decision  string `json:"decision"`
			Rationale string `json:"rationale"`
		} `json:"nodes"`
	} `json:"tree"`
	CodeState struct {
		Head  string `json:"head"`
		Dirty bool   `json:"dirty"`
	} `json:"code_state"`
}

func (st *runState) latestPacketPath(ctx context.Context, project string) (string, string) {
	res := st.anchor(ctx, project, "handoff", "status", "--dir", project, "--repo", project)
	if res.ExitCode == 0 {
		var status struct {
			Path      string `json:"path"`
			HandoffID string `json:"handoff_id"`
		}
		if err := json.Unmarshal([]byte(res.Output), &status); err == nil && status.Path != "" {
			return status.Path, status.HandoffID
		}
	}
	return "", ""
}

func (st *runState) prepareHandoff(ctx context.Context, project string) (string, string, error) {
	res := st.anchor(ctx, project, "handoff", "prepare", "--dir", project, "--repo", project)
	if res.ExitCode != 0 {
		return "", "", fmt.Errorf("anchor handoff prepare: %s", strings.TrimSpace(res.Output))
	}
	var out struct {
		Path      string `json:"path"`
		HandoffID string `json:"handoff_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil || out.Path == "" {
		return "", "", fmt.Errorf("anchor handoff prepare: unreadable output: %s", strings.TrimSpace(res.Output))
	}
	b, err := os.ReadFile(out.Path)
	if err != nil {
		return "", "", err
	}
	return out.Path, string(b), nil
}

// ensureHandoff makes sure the packet exists and matches the committed
// post-phase-1 working tree. Normally the phase-1 agent's wrap prepared it; if
// the harness had to restore tampered visible tests (or the agent left the tree
// dirty), the packet is refreshed so successor ack/verify can be exact.
func (st *runState) ensureHandoff(ctx context.Context, project, stateDir string, variant *LoaderVariant, row *TrialRow) (string, string, int, error) {
	packetPath, _ := st.latestPacketPath(ctx, project)
	packetJSON := ""
	if packetPath != "" {
		if b, err := os.ReadFile(packetPath); err == nil {
			packetJSON = string(b)
		}
	}
	preparedBy := "agent_wrap"
	if packetPath == "" {
		// Fallback: the wrap could not prepare (or the agent removed the packet).
		path, body, err := st.prepareHandoff(ctx, project)
		if err != nil {
			return "", "", 0, err
		}
		packetPath, packetJSON = path, body
		preparedBy = "harness_fallback"
	}
	head, err := gitHead(project)
	if err != nil {
		return packetPath, packetJSON, 0, nil
	}
	var pv packetView
	_ = json.Unmarshal([]byte(packetJSON), &pv)
	stale := pv.CodeState.Head != head || pv.CodeState.Dirty
	if stale {
		path, body, err := st.prepareHandoff(ctx, project)
		if err != nil {
			return packetPath, packetJSON, 0, fmt.Errorf("refresh stale packet: %w", err)
		}
		packetPath, packetJSON = path, body
		preparedBy = "harness_refresh"
		row.HandoffRefreshed = true
		_ = json.Unmarshal([]byte(packetJSON), &pv)
	}
	row.HandoffPreparedBy = preparedBy
	count := countPacketDecisions(&pv, variant)
	return packetPath, packetJSON, count, nil
}

func countPacketDecisions(pv *packetView, variant *LoaderVariant) int {
	count := 0
	for _, d := range pv.Decisions {
		if variantInText(variant, d.ID+"\n"+d.Title+"\n"+d.Decision+"\n"+d.Rationale) {
			count++
		}
	}
	if count > 0 {
		return count
	}
	for _, n := range pv.Tree.Nodes {
		if n.Type != "decision" {
			continue
		}
		if variantInText(variant, n.ID+"\n"+n.Title+"\n"+n.Decision+"\n"+n.Rationale) {
			count++
		}
	}
	return count
}

// restoreHandoff runs the full-arm successor acknowledgements before phase 2.
func (st *runState) restoreHandoff(ctx context.Context, project, dir, packetPath string, row *TrialRow) (bool, bool) {
	ackRes := runLogged(ctx, st.cfg.Timeout, project, baseEnv(nil), filepath.Join(dir, "log_restore_ack.log"),
		st.anchorBin, "handoff", "restore", "--dir", project, "--repo", project, "--packet", packetPath, "--ack")
	row.Artifacts["restore_ack"] = relPath(st.outDir, filepath.Join(dir, "log_restore_ack.log"))
	if ackRes.ExitCode != 0 {
		return false, false
	}
	verifyRes := runLogged(ctx, st.cfg.Timeout, project, baseEnv(nil), filepath.Join(dir, "log_restore_verify.log"),
		st.anchorBin, "handoff", "restore", "--dir", project, "--repo", project, "--packet", packetPath, "--verify")
	row.Artifacts["restore_verify"] = relPath(st.outDir, filepath.Join(dir, "log_restore_verify.log"))
	return true, verifyRes.ExitCode == 0
}

// ---------------------------------------------------------------------------
// go test acceptance

func (st *runState) runGoTest(ctx context.Context, project, logName string) commandResult {
	logPath := filepath.Join(project, "..", logName)
	res := runLogged(ctx, st.cfg.Timeout, project, baseEnv(nil), logPath, st.goBin, "test", "-count=1", "./...")
	return res
}

// ---------------------------------------------------------------------------
// Git helpers

func gitEnv() []string {
	return baseEnv(map[string]string{
		"GIT_AUTHOR_NAME":     "stress-bench",
		"GIT_AUTHOR_EMAIL":    "bench@stress.invalid",
		"GIT_COMMITTER_NAME":  "stress-bench",
		"GIT_COMMITTER_EMAIL": "bench@stress.invalid",
		"GIT_AUTHOR_DATE":     "2000-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE":  "2000-01-01T00:00:00Z",
	})
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func gitInitCommit(dir string) (string, error) {
	if _, err := gitRun(dir, "init", "-q"); err != nil {
		return "", err
	}
	if _, err := gitRun(dir, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return "", err
	}
	if _, err := gitRun(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := gitRun(dir, "commit", "-q", "-m", "initial fixture state"); err != nil {
		return "", err
	}
	return gitHead(dir)
}

func gitHead(dir string) (string, error) {
	out, err := gitRun(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitCommitIfDirty(dir, message string) (bool, string, error) {
	out, err := gitRun(dir, "status", "--porcelain")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(out) == "" {
		return false, "", nil
	}
	if _, err := gitRun(dir, "add", "-A"); err != nil {
		return false, "", err
	}
	if _, err := gitRun(dir, "commit", "-q", "-m", message); err != nil {
		return false, "", err
	}
	head, err := gitHead(dir)
	if err != nil {
		return false, "", err
	}
	return true, head, nil
}

func gitDiffFrom(dir, from string) (string, error) {
	out, err := gitRun(dir, "diff", "--no-color", from, "HEAD")
	if err != nil {
		return "", err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Filesystem helpers

// copyTree copies src into dst (dst may not exist), preserving regular file
// modes. It ignores .git directories.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info, err := d.Info(); err == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
}

// restoreVisibleTests rewrites the canonical visible test files when an agent
// modified (or deleted) them, and reports whether it had to.
func restoreVisibleTests(project string, fx *Fixture) (bool, []string, error) {
	names := make([]string, 0, len(fx.CanonicalTests))
	for name := range fx.CanonicalTests {
		names = append(names, name)
	}
	sort.Strings(names)
	var restored []string
	for _, name := range names {
		want := fx.CanonicalTests[name]
		path := filepath.Join(project, name)
		got, err := os.ReadFile(path)
		if err == nil && string(got) == string(want) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return len(restored) > 0, restored, err
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return len(restored) > 0, restored, err
		}
		restored = append(restored, name)
	}
	return len(restored) > 0, restored, nil
}
