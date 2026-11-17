package main

import (
	"fmt"
	"math"
	"sort"
)

// summarize aggregates trial rows into per-arm summaries.
func summarize(rows []TrialRow, arms []string, trials int) map[string]*ArmSummary {
	out := make(map[string]*ArmSummary, len(arms))
	for _, arm := range arms {
		out[arm] = &ArmSummary{Arm: arm, VariantsUsed: map[string]int{}}
	}
	for _, row := range rows {
		a := out[row.Arm]
		if a == nil {
			a = &ArmSummary{Arm: row.Arm, VariantsUsed: map[string]int{}}
			out[row.Arm] = a
		}
		a.Trials++
		if row.Phase1Pass {
			a.Phase1Pass++
		}
		if row.Phase2Pass {
			a.Phase2Pass++
		}
		if row.DecisionsRecorded {
			a.DecisionsRecorded++
		}
		a.PacketDecisions += row.PacketDecisions
		if row.VariantConveyed {
			a.VariantConveyed++
		}
		if row.VariantLeakedInRepo {
			a.VariantLeaks++
		}
		if row.TestsModified {
			a.TestsModified++
		}
		if row.HandoffVerified {
			a.HandoffVerified++
		}
		a.MeanTokens += float64(row.Tokens)
		a.MeanCostUSD += row.CostUSD
		a.VariantsUsed[row.Variant]++
	}
	for arm, a := range out {
		if a.Trials > 0 {
			a.Phase1PassAt1 = float64(a.Phase1Pass) / float64(a.Trials)
			a.Phase2PassAt1 = float64(a.Phase2Pass) / float64(a.Trials)
			a.MeanTokens /= float64(a.Trials)
			a.MeanCostUSD /= float64(a.Trials)
		}
		k := trials
		if k > a.Trials {
			k = a.Trials
		}
		if k < 1 {
			k = 1
		}
		a.PassK = k
		a.Phase2PassK = passK(a.Phase2Pass, a.Trials, k)
		a.Phase1CI = wilson(a.Phase1Pass, a.Trials)
		a.Phase2CI = wilson(a.Phase2Pass, a.Trials)
		_ = arm
	}
	return out
}

// summarizePerTask produces task -> arm -> pass counts so a report shows where
// discrimination comes from instead of only a global average.
func summarizePerTask(rows []TrialRow) map[string]TaskSummary {
	out := map[string]TaskSummary{}
	for _, row := range rows {
		ts, ok := out[row.Task]
		if !ok {
			ts = TaskSummary{Task: row.Task, Arms: map[string]*TaskArmStats{}}
			out[row.Task] = ts
		}
		stat := ts.Arms[row.Arm]
		if stat == nil {
			stat = &TaskArmStats{}
			ts.Arms[row.Arm] = stat
		}
		stat.Trials++
		if row.Phase1Pass {
			stat.Phase1Pass++
		}
		if row.Phase2Pass {
			stat.Phase2Pass++
		}
	}
	for task, ts := range out {
		for _, stat := range ts.Arms {
			if stat.Trials > 0 {
				stat.Phase2PassAt1 = float64(stat.Phase2Pass) / float64(stat.Trials)
			}
		}
		out[task] = ts
	}
	return out
}

// wilson returns the 95% Wilson score interval for successes/n.
func wilson(successes, n int) WilsonCI {
	if n <= 0 {
		return WilsonCI{}
	}
	const z = 1.959963984540054
	p := float64(successes) / float64(n)
	nn := float64(n)
	denom := 1 + z*z/nn
	center := p + z*z/(2*nn)
	margin := z * math.Sqrt(p*(1-p)/nn+z*z/(4*nn*nn))
	lo := (center - margin) / denom
	hi := (center + margin) / denom
	return WilsonCI{Lower: clamp01(lo), Upper: clamp01(hi)}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// passK estimates the probability that all k independent trials pass, given c
// successes out of n observed trials: C(c,k)/C(n,k). It is zero when c < k.
func passK(successes, n, k int) float64 {
	if n <= 0 || k <= 0 || k > n || successes < k {
		return 0
	}
	ratio := 1.0
	for i := 0; i < k; i++ {
		ratio *= float64(successes-i) / float64(n-i)
	}
	return ratio
}

// orderedTasks returns deterministic task names from a per-task map.
func orderedTasks(perTask map[string]TaskSummary) []string {
	names := make([]string, 0, len(perTask))
	for name := range perTask {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// roundUSD strips floating-point accumulation noise from reported costs.
func roundUSD(v float64) float64 { return math.Round(v*1e9) / 1e9 }

// fmtPct formats a [0,1] value as a percentage with one decimal.
func fmtPct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

// fmtCI formats a Wilson interval as "low–high".
func fmtCI(ci WilsonCI) string { return fmt.Sprintf("%.1f%%–%.1f%%", ci.Lower*100, ci.Upper*100) }
