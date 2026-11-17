package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runMockAgent is the offline scripted agent used by --mock-agent. It is a
// genuine separate process (the same executable re-entered with an environment
// marker) so phase 1 and phase 2 really run across a process boundary.
//
// The mock's phase-2 behaviour is deliberately mechanical: it writes the
// convention-correct file only when the context it was actually given (prompt,
// wrap context, handoff packet) contains the variant id or statement. Otherwise
// it writes a naive implementation that is wrong for every variant. That makes
// the ablation a property of the conveyed context, not of the mock's knowledge.
func runMockAgent() int {
	planPath := os.Getenv(envMockPlan)
	if planPath == "" {
		fmt.Fprintln(os.Stderr, "mock-agent: STRESS_MOCK_PLAN is not set")
		return 2
	}
	b, err := os.ReadFile(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-agent: read plan: %v\n", err)
		return 2
	}
	var plan mockPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		fmt.Fprintf(os.Stderr, "mock-agent: parse plan: %v\n", err)
		return 2
	}
	phase := plan.Phase
	if raw := strings.TrimSpace(os.Getenv(envPhase)); raw != "" {
		fmt.Sscanf(raw, "%d", &phase)
	}
	switch phase {
	case 1:
		if err := mockPhase1(&plan); err != nil {
			fmt.Fprintf(os.Stderr, "mock-agent phase1: %v\n", err)
			return 1
		}
	case 2:
		if err := mockPhase2(&plan); err != nil {
			fmt.Fprintf(os.Stderr, "mock-agent phase2: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "mock-agent: unknown phase %d\n", phase)
		return 2
	}
	fmt.Printf("mock-agent phase=%d task=%s arm=%s mode=%s\n", phase, plan.Task, plan.Arm, plan.MockMode)
	printMockUsage(phase, plan)
	return 0
}

func mockPhase1(plan *mockPlan) error {
	code := plan.Phase1Code
	if plan.MockMode == mockModeSabotage && plan.Phase1CodeBad != "" {
		code = plan.Phase1CodeBad
	}
	if err := os.WriteFile(filepath.Join(plan.Project, plan.Phase1File), []byte(code), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", plan.Phase1File, err)
	}
	if plan.MockMode == mockModeSabotage {
		if err := tamperVisibleTests(plan); err != nil {
			return err
		}
	}
	if len(plan.Decisions) > 0 && plan.AnchorWorkspace != "" {
		for _, d := range plan.Decisions {
			if err := recordDecision(plan, d); err != nil {
				return err
			}
		}
	}
	if plan.Commit {
		if err := gitCommitAll(plan.Project, plan.CommitMessage); err != nil {
			return err
		}
	}
	return nil
}

func mockPhase2(plan *mockPlan) error {
	conveyed := contextConveysVariant(plan)
	code := plan.Phase2Wrong
	// The cheater writes the naive file and tampers with the visible tests; it is
	// detected and the canonical tests are restored before acceptance.
	if conveyed && plan.MockMode != mockModeCheater {
		code = plan.Phase2Correct
	}
	if err := os.WriteFile(filepath.Join(plan.Project, plan.Phase2File), []byte(code), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", plan.Phase2File, err)
	}
	if plan.MockMode == mockModeCheater {
		if err := tamperVisibleTests(plan); err != nil {
			return err
		}
	}
	fmt.Printf("mock-agent conveyed=%v variant=%s\n", conveyed, plan.Variant)
	return nil
}

// contextConveysVariant reads every context source the phase-2 session was
// given and reports whether the hidden convention is present in it.
func contextConveysVariant(plan *mockPlan) bool {
	var texts []string
	paths := append([]string{plan.PromptFile, plan.Packet, plan.RulesFile}, plan.ContextFiles...)
	for _, p := range paths {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			texts = append(texts, string(b))
		}
	}
	combined := strings.Join(texts, "\n")
	if plan.Variant != "" && strings.Contains(combined, plan.Variant) {
		return true
	}
	if plan.VariantPhrase != "" && strings.Contains(combined, plan.VariantPhrase) {
		return true
	}
	return false
}

// recordDecision records one decision node through the real anchor CLI, exactly
// as the phase-1 prompt instructs a real agent to do.
func recordDecision(plan *mockPlan, d mockDecision) error {
	anchorBin := plan.AnchorBin
	if anchorBin == "" {
		return fmt.Errorf("anchor binary is not configured for the mock agent")
	}
	args := []string{
		"node", "create",
		"--dir", plan.AnchorWorkspace,
		"--id", d.ID,
		"--type", "decision",
		"--parent", d.Parent,
		"--title", d.Title,
		"--decision", d.Decision,
		"--rationale", d.Rationale,
	}
	cmd := exec.Command(anchorBin, args...)
	cmd.Dir = plan.Project
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("anchor node create %s: %v: %s", d.ID, err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("mock-agent recorded decision %s: %s\n", d.ID, strings.TrimSpace(string(out)))
	return nil
}

func tamperVisibleTests(plan *mockPlan) error {
	for i, name := range plan.VisibleTests {
		content := fmt.Sprintf("package %s\n\nimport \"testing\"\n\nfunc TestBenchTampered%d(t *testing.T) {}\n", plan.PackageName, i)
		if err := os.WriteFile(filepath.Join(plan.Project, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("tamper %s: %w", name, err)
		}
		fmt.Printf("mock-agent tampered visible test %s\n", name)
	}
	return nil
}

func gitCommitAll(dir, message string) error {
	if message == "" {
		message = "phase 1 work"
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	commit := exec.Command("git", "-c", "user.name=stress-agent", "-c", "user.email=agent@stress.invalid",
		"commit", "-q", "-m", message)
	commit.Dir = dir
	out, err := commit.CombinedOutput()
	if err != nil {
		// Nothing to commit is not an error for the mock agent.
		if strings.Contains(string(out), "nothing to commit") || strings.Contains(string(out), "no changes added") {
			return nil
		}
		return fmt.Errorf("git commit: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func printMockUsage(phase int, plan mockPlan) {
	input, output := int64(1200), int64(300)
	if phase == 2 {
		input, output = 900, 400
	}
	usage := map[string]any{
		"mock_agent":     true,
		"phase":          phase,
		"model":          "mock-agent",
		"num_turns":      1,
		"total_cost_usd": plan.Usage.CostUSD,
		"usage": map[string]any{
			"input_tokens":  input,
			"output_tokens": output,
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(usage)
}
