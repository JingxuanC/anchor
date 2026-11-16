package main

import "testing"

func TestParseTokenUsage(t *testing.T) {
	output := `some agent chatter
{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"num_turns":3,"result":"done","total_cost_usd":0.0123,"usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":50}}
`
	u := parseTokenUsage(output)
	if !u.Parsed || u.Input != 100 || u.Output != 50 || u.CacheCreate != 200 || u.CacheRead != 300 {
		t.Fatalf("usage parsed wrong: %+v", u)
	}
	if u.Total() != 650 {
		t.Fatalf("total = %d, want 650", u.Total())
	}
	if u.CostUSD != 0.0123 || u.Turns != 3 {
		t.Fatalf("cost/turns wrong: %+v", u)
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
{"type":"result","usage":{"input_tokens":10,"output_tokens":20},"total_cost_usd":0.5}
`
	u := parseTokenUsage(output)
	if u.Input != 10 || u.Output != 20 || u.CostUSD != 0.5 {
		t.Fatalf("last result should win: %+v", u)
	}
}
