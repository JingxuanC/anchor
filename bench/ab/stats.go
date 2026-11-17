package main

import (
	"math"
	"sort"
)

// wilson95 returns the Wilson score interval for successes/n at 95% confidence.
// It is well behaved for small n and extreme proportions (unlike the normal
// approximation), which is what benchmark pass rates usually are.
func wilson95(successes, n int) (low, high float64) {
	if n <= 0 {
		return 0, 0
	}
	const z = 1.959963984540054
	p := float64(successes) / float64(n)
	denom := 1 + z*z/float64(n)
	centre := p + z*z/(2*float64(n))
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*float64(n)))/float64(n))
	return math.Max(0, (centre-margin)/denom), math.Min(1, (centre+margin)/denom)
}

// passK reports whether all k trials for the task passed (reliability, not
// "at least one"). With no trials it returns false.
func passK(passes []bool) bool {
	if len(passes) == 0 {
		return false
	}
	for _, p := range passes {
		if !p {
			return false
		}
	}
	return true
}

// mean returns the arithmetic mean (0 for an empty slice).
func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// median returns the median (0 for an empty slice).
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64{}, xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// pairedDelta is the anchor-minus-baseline difference on paired trials, with a
// task/run-level bootstrap 95% interval. Trials are paired by trial index; the
// bootstrap resamples trials with replacement. It is a descriptive interval for
// small N, not a significance test.
func pairedDelta(baseline, anchor []float64, resamples int) (delta, low, high float64) {
	n := len(baseline)
	if n == 0 || len(anchor) != n {
		return 0, 0, 0
	}
	if resamples <= 0 {
		resamples = 2000
	}
	var d float64
	for i := 0; i < n; i++ {
		d += anchor[i] - baseline[i]
	}
	delta = d / float64(n)
	// Deterministic xorshift PRNG keeps reports reproducible without math/rand
	// seeding surprises.
	state := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	samples := make([]float64, 0, resamples)
	for r := 0; r < resamples; r++ {
		var sum float64
		for i := 0; i < n; i++ {
			idx := int(next() % uint64(n))
			sum += anchor[idx] - baseline[idx]
		}
		samples = append(samples, sum/float64(n))
	}
	sort.Float64s(samples)
	lo := int(math.Floor(0.025 * float64(len(samples))))
	hi := int(math.Ceil(0.975*float64(len(samples)))) - 1
	if lo < 0 {
		lo = 0
	}
	if hi >= len(samples) {
		hi = len(samples) - 1
	}
	return delta, samples[lo], samples[hi]
}

// mcnemarExactP returns the exact two-sided McNemar p-value for paired binary
// outcomes, treating discordant pairs as a binomial sample under p=0.5. It is
// reported alongside the delta for transparency without claiming power.
func mcnemarExactP(bOnly, aOnly int) float64 {
	n := bOnly + aOnly
	if n == 0 {
		return 1
	}
	k := bOnly
	if aOnly < k {
		k = aOnly
	}
	// Two-sided exact binomial tail: 2 * sum_{i=0..k} C(n,i) / 2^n.
	sum := 0.0
	term := 1.0
	for i := 0; i <= k; i++ {
		if i > 0 {
			term = term * float64(n-i+1) / float64(i)
		}
		sum += term
	}
	p := 2 * sum / math.Pow(2, float64(n))
	if p > 1 {
		p = 1
	}
	return p
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// phasePairs aligns baseline and anchor trials by trial number for one phase and
// returns per-trial 0/1 outcomes plus discordant-pair counts for McNemar.
func phasePairs(trials []TrialResult, phase int) (baseline, anchor []float64, bOnly, aOnly int) {
	type row struct {
		b, a       bool
		hasB, hasA bool
	}
	byTrial := map[int]*row{}
	for _, t := range trials {
		r := byTrial[t.Trial]
		if r == nil {
			r = &row{}
			byTrial[t.Trial] = r
		}
		pass := t.Phase1Pass
		if phase == 2 {
			pass = t.Phase2Pass
		}
		switch t.Arm {
		case armBaseline:
			r.b, r.hasB = pass, true
		case armAnchor:
			r.a, r.hasA = pass, true
		}
	}
	ids := make([]int, 0, len(byTrial))
	for id := range byTrial {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		r := byTrial[id]
		if !r.hasB || !r.hasA {
			continue
		}
		baseline = append(baseline, boolFloat(r.b))
		anchor = append(anchor, boolFloat(r.a))
		if r.b && !r.a {
			bOnly++
		}
		if r.a && !r.b {
			aOnly++
		}
	}
	return baseline, anchor, bOnly, aOnly
}
