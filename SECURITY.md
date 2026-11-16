# Security policy

Anchor is a local, offline tool. It has no network calls, no telemetry, and no
third-party dependencies; the only subprocesses it runs are `git` and the agent
CLI you explicitly ask it to run.

## Reporting a vulnerability

Please do **not** open a public issue for a security problem. Use GitHub's
private vulnerability reporting ("Security" tab → "Report a vulnerability") or
email the maintainer listed in the repository profile.

Include: affected version/commit, a minimal reproducer, and the impact (e.g.
path escape, secret leakage, gate bypass). We aim to acknowledge within a few
days.

## What Anchor guarantees / does not guarantee

- Package/export files are hash-verified and text-scanned for secrets before
  import; `apply-code` never executes package content and validates every hash
  before mutating a repository.
- Captured adapter events are metadata only: prompts, tool inputs and
  transcripts are dropped by design.
- Anchor is **not** a sandbox. An agent with filesystem permissions can still
  modify the workspace; the ownership/gate checks defend against accidental
  concurrent writes and stale resumes, not against a hostile local process.
- A git bundle inside an export can contain committed secrets from the source
  repository history; committed history is not text-scanned. Verify provenance
  before sharing packages.
