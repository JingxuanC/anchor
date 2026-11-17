package main

import (
	"math"
	"testing"
)

func TestWilson95Bounds(t *testing.T) {
	if _, high := wilson95(0, 3); high <= 0 || high >= 0.8 {
		t.Fatalf("0/3 upper bound = %v", high)
	}
	if low, _ := wilson95(3, 3); low <= 0.2 || low >= 1 {
		t.Fatalf("3/3 lower bound = %v", low)
	}
	lo, hi := wilson95(5, 10)
	if math.Abs(lo-0.2366) > 0.01 || math.Abs(hi-0.7634) > 0.01 {
		t.Fatalf("5/10 interval = [%v,%v]", lo, hi)
	}
	lo0, hi0 := wilson95(0, 0)
	if lo0 != 0 || hi0 != 0 {
		t.Fatalf("empty interval = [%v,%v]", lo0, hi0)
	}
}

func TestPassK(t *testing.T) {
	if !passK([]bool{true, true, true}) {
		t.Fatal("all pass should be pass^k")
	}
	if passK([]bool{true, false, true}) {
		t.Fatal("one failure should break pass^k")
	}
	if passK(nil) {
		t.Fatal("no trials cannot pass^k")
	}
}

func TestPairedDeltaDeterministicAndBounded(t *testing.T) {
	base := []float64{0, 0, 1, 0}
	anchor := []float64{1, 1, 1, 0}
	d, lo, hi := pairedDelta(base, anchor, 500)
	if math.Abs(d-0.5) > 1e-9 {
		t.Fatalf("delta = %v", d)
	}
	if lo > d || hi < d || lo < -1 || hi > 1 {
		t.Fatalf("interval [%v,%v] does not contain %v", lo, hi, d)
	}
	d2, lo2, hi2 := pairedDelta(base, anchor, 500)
	if d != d2 || lo != lo2 || hi != hi2 {
		t.Fatal("bootstrap is not deterministic")
	}
	if _, _, _ = pairedDelta(nil, nil, 10); true {
		// no panic on empty input
	}
}

func TestMcNemarExactP(t *testing.T) {
	if p := mcnemarExactP(0, 0); p != 1 {
		t.Fatalf("no discordant pairs p = %v", p)
	}
	// 0 vs 5 discordant pairs: two-sided exact p = 2*(1/32) = 0.0625.
	if p := mcnemarExactP(5, 0); math.Abs(p-0.0625) > 1e-9 {
		t.Fatalf("5/0 p = %v", p)
	}
	if p := mcnemarExactP(3, 3); p < 0.9 || p > 1 {
		t.Fatalf("balanced discordant p = %v", p)
	}
}
