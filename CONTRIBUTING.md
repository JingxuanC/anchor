# Contributing

Thanks for helping! Anchor is a small, dependency-free Go project; keeping it
that way is a feature.

## Development setup

```sh
git clone https://github.com/JingxuanC/anchor
cd anchor
go build ./...
go test ./... -count=1
go vet ./...
gofmt -l cmd internal bench   # must be empty
```

`go test -race ./...` is expected to pass (it takes a few minutes). The A/B
harness has an offline self-test: `go run ./bench/ab --mock-agent --trials 1`.

## Ground rules

- **No new third-party dependencies** and no network/telemetry code.
- **No secret capture**: adapter capture stays metadata-only; never store
  prompts, tool inputs, transcripts or tokens.
- **Fail closed**: security-relevant paths (paths, refs, leases, gate, hashes)
  must reject on doubt, not silently continue.
- **Tests assert real behavior.** Do not weaken an assertion to make a change
  pass; fix the code or explain the finding in the PR.
- Keep user-facing status honest: `incomplete`/`unverified` labels must reflect
  what was actually verified.

## Pull requests

1. Fork, create a topic branch, keep commits focused.
2. Add tests for behavior changes and update `docs/09-implementation-status.md`
   when a requirement's status changes.
3. Run `gofmt`, `go vet`, `go test ./...` (and `-race` for concurrency changes)
   before opening the PR.
4. Describe the problem, the change, and any trade-offs in the PR body.

CI runs gofmt/vet/tests on Linux and macOS, `-race` on Linux, and cross-builds
Windows/Linux/macOS binaries.

## Documentation

Design documents live in `docs/` (start with `docs/01-architecture.md` and
`docs/09-implementation-status.md`). The audit trail in `docs/09` is the
authoritative status; keep it current.
