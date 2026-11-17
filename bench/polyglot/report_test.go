package main

import (
	"math"
	"testing"
)

func TestParseTokenUsage(t *testing.T) {
	output := `some agent chatter
{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"num_turns":3,"result":"done","model":"claude-test","total_cost_usd":0.0123,"usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":50}}
`
	u := parseTokenUsage(output)
	if !u.Parsed || u.Input != 100 || u.Output != 50 || u.CacheCreate != 200 || u.CacheRead != 300 {
		t.Fatalf("usage parsed wrong: %+v", u)
	}
	if u.Total() != 650 {
		t.Fatalf("total = %d, want 650", u.Total())
	}
	if u.CostUSD != 0.0123 || u.Turns != 3 || u.Model != "claude-test" {
		t.Fatalf("cost/turns/model wrong: %+v", u)
	}

	// No usage object: not parsed, all zero.
	if got := parseTokenUsage("plain text, no json\n"); got.Parsed || got.Total() != 0 {
		t.Fatalf("unexpected usage from plain text: %+v", got)
	}
	// A malformed JSON line is ignored rather than fatal.
	if got := parseTokenUsage("{not json}\n{\"usage\":{\"input_tokens\":1}}\n"); !got.Parsed || got.Input != 1 {
		t.Fatalf("malformed-line handling wrong: %+v", got)
	}
}

func TestParseTokenUsageLastResultWins(t *testing.T) {
	output := `{"usage":{"input_tokens":1,"output_tokens":1}}
{"type":"result","usage":{"input_tokens":10,"output_tokens":20},"total_cost_usd":0.5,"modelUsage":{"zzz-model":{"inputTokens":1},"aaa-model":{"inputTokens":1}}}
`
	u := parseTokenUsage(output)
	if u.Input != 10 || u.Output != 20 || u.CostUSD != 0.5 {
		t.Fatalf("last result should win: %+v", u)
	}
	if u.Model != "aaa-model" {
		t.Fatalf("modelUsage fallback should pick a deterministic model, got %q", u.Model)
	}
}

func TestWilsonCI(t *testing.T) {
	// 1/1 successes: interval must contain 1 and have a lower bound below 1.
	ci := wilsonCI(1, 1)
	if ci.Low < 0 || ci.High > 1 || ci.Low > ci.High {
		t.Fatalf("1/1 CI = %+v", ci)
	}
	if math.Abs(ci.Low-0.2065) > 0.01 {
		t.Fatalf("1/1 lower bound = %v, want about 0.2065", ci.Low)
	}
	// 0/0 has no interval.
	if ci := wilsonCI(0, 0); ci.Low != 0 || ci.High != 0 {
		t.Fatalf("0/0 CI = %+v", ci)
	}
	// 5/10 is centered around 0.5.
	if ci := wilsonCI(5, 10); ci.Low >= 0.5 || ci.High <= 0.5 {
		t.Fatalf("5/10 CI = %+v, want it to straddle 0.5", ci)
	}
}

func TestAggregatePassAtK(t *testing.T) {
	tasks := []Task{{Slug: "a", Lang: "python"}, {Slug: "b", Lang: "python"}}
	rows := []TrialResult{
		{Task: "a", Arm: armBaseline, Trial: 1, Pass: true, WallMS: 10, Tokens: 5, CostUSD: 0.01},
		{Task: "a", Arm: armBaseline, Trial: 2, Pass: true, WallMS: 20, Tokens: 5, CostUSD: 0.01},
		{Task: "b", Arm: armBaseline, Trial: 1, Pass: true, WallMS: 30, Tokens: 5, CostUSD: 0.01},
		{Task: "b", Arm: armBaseline, Trial: 2, Pass: false, WallMS: 40, Tokens: 5, CostUSD: 0.01, TestsModified: true},
	}
	perTask, agg := aggregateTrials(rows, tasks, []string{armBaseline}, 2)
	base := agg[armBaseline]
	if base.Tasks != 2 || base.Trials != 4 || base.Passed != 3 {
		t.Fatalf("aggregate = %+v", base)
	}
	if base.Pass1 != 0.75 {
		t.Fatalf("pass@1 = %v, want 0.75", base.Pass1)
	}
	if base.PassKEligible != 2 || base.PassKTasks != 1 || base.PassK != 0.5 {
		t.Fatalf("pass@k = %v (%d/%d), want 0.5 (1/2)", base.PassK, base.PassKTasks, base.PassKEligible)
	}
	if base.MeanWallMS != 25 || base.MeanTokens != 5 || base.TestsModified != 1 {
		t.Fatalf("means = %+v", base)
	}
	if base.Pass1CI.Low >= 0.75 || base.Pass1CI.High <= 0.75 {
		t.Fatalf("CI should straddle the point estimate: %+v", base.Pass1CI)
	}
	if len(perTask) != 2 {
		t.Fatalf("per-task entries = %d", len(perTask))
	}
	if stats := perTask[1].Arms[armBaseline]; stats.Trials != 2 || stats.Passed != 1 || stats.PassK {
		t.Fatalf("task b stats = %+v", stats)
	}
}

func TestBudgetExceeded(t *testing.T) {
	b := &Budget{MaxUSD: 1}
	if b.Exceeded() {
		t.Fatal("empty budget should not be exceeded")
	}
	b.Add(10, 0.5)
	if b.Exceeded() {
		t.Fatal("0.5/1 should not be exceeded")
	}
	b.Add(10, 0.5)
	if !b.Exceeded() {
		t.Fatal("1/1 should be exceeded")
	}
	// Zero caps mean unlimited.
	unlimited := &Budget{}
	unlimited.Add(1e9, 1e9)
	if unlimited.Exceeded() {
		t.Fatal("zero caps must mean unlimited")
	}
	// Token cap only.
	tokens := &Budget{MaxTokens: 100}
	tokens.Add(100, 0)
	if !tokens.Exceeded() {
		t.Fatal("token cap should be exceeded")
	}
}
