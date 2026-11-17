# Offline Aider-polyglot fixtures

These fixtures are authored for this repository's benchmark harness. They are
deliberately tiny and are **not** copied from the Exercism problem statements
that Aider's polyglot benchmark uses; only the well-known exercise names
(`leap`, `two_fer`) and their standard rules are shared.

## Layout

```
python/
  leap/
    .docs/instructions.md     task description handed to the agent
    leap.py                   stub source file (fails the tests)
    leap_test.py              visible acceptance tests (pytest, plain asserts)
    mock/correct/leap.py      solution copied by the offline mock agent
    mock/cheat/leap_test.py   test file copied by the cheating mock agent
  two_fer/
    ...
```

The harness discovers every directory that has `.docs/instructions.md` and
recognises the language from the source/test files. It never copies `mock/`
into the agent's project, so the correct solution is not visible to the agent.

`mock/correct/` is only used by `--mock-agent --mock-mode normal`;
`mock/cheat/` is only used by `--mock-agent --mock-mode cheater`, which exists
to prove that the harness detects modified test files and restores them before
grading.

Run the offline demo from the repository root:

```sh
go run ./bench/polyglot --mock-agent --trials 1 --out /tmp/poly
go run ./bench/polyglot --mock-agent --mock-mode cheater --trials 1 --out /tmp/poly-cheat
```
