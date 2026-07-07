# Agent Instructions

## What This Is

Ocel — "Platform as a Framework." An SDK that turns cloud infrastructure into function
calls: call `postgres("main")` in app code and that call *is* the provisioning step.
`ocel dev` connects it to a real, running instance instantly (no Docker, no emulators);
on deploy, the same code compiles to real infrastructure-as-code and lands in the
user's own AWS/GCP account, not a vendor-owned black box. Full product docs (philosophy,
comparisons, PRDs) live in the sibling `docs` repo — read there before making
product-framing claims; this file only covers what an agent needs to build in *this*
repo.

## Architecture Overview

- `ocel/` — Go CLI (cobra: `login`/`logout`/`dev`/`init`). `login` runs RFC 8628
  device-authorization against the control plane. `dev` resolves project config, starts
  a local dev server, bundles the app's `ocel/` resource declarations with esbuild and
  runs them in Node to discover what's declared, provisions those resources, then execs
  the app command with the resolved env injected.
- `packages/ocel` — the TS SDK (`ocel`, `ocel/postgres`) apps import to declare
  resources; talks to the CLI's local dev server during `ocel dev`.
- `packages/native-lib` — prebuilt per-platform CLI binaries, wired as `ocel`'s
  `optionalDependencies` (npm's platform-package pattern). No cgo deps, so CI
  cross-compiles all 4 targets from one Linux runner (`scripts/build-native.mjs`).
- `apps/web` — Next.js control plane: better-auth (email/password, GitHub OAuth,
  device-authorization + bearer plugins) on Drizzle/Postgres; `/api/projects` routes.
- `proto/` — protobuf source of truth (buf), codegen'd into `ocel/pkg/proto` (Go) and
  `packages/ocel/src/gen` (TS) via `pnpm gen`. Edit `.proto` files, then regenerate —
  never hand-edit generated output.

**Status vs. the product docs:** the `docs` repo describes the target design (e.g. real
provisioning, monorepo leader/follower dev mode). Several pieces here are still
stubs with finalized signatures — e.g. `ocel init` and `internal/provision` — built
ahead of the real backend/API. Check the code, not just the docs, before assuming a
described behavior is live.

## Build & Test

```bash
pnpm install                 # JS deps (pnpm workspace: apps/*, packages/*, packages/native-lib/*)
pnpm gen                     # regenerate proto bindings after editing proto/**
cd ocel && go build ./...    # build the CLI
cd ocel && go test ./...     # Go tests
cd apps/web && pnpm test     # vitest
cd apps/web && pnpm lint     # biome check
docker compose up -d         # local Postgres for apps/web
```

## Conventions & Patterns

- Go workspace (`go.work`) covers `./ocel` only; JS side is the pnpm workspace.
- Versioning goes through Changesets (`.changeset/`); `pnpm ci:version` is run by the
  release workflow, not by hand.
- When wiring backend-dependent Go code ahead of the real backend/API, stub it with
  final signatures (see `internal/provision`'s doc comment) so callers never need to
  change later — don't leave TODOs in caller-facing shapes.
- If you discover context while working a feature that would help downstream work or
  other agents — a gotcha, a convention, a stub's real status, a doc that turned out to
  live elsewhere — fold it into this file and `CLAUDE.md` before finishing, not just
  into a commit message or PR description.

This project uses **bd** (beads) for issue tracking. Run `bd prime` for full workflow context.

> **Architecture in one line:** Issues live in a local Dolt database
> (`.beads/dolt/`); cross-machine sync uses `bd dolt push/pull` (a
> git-compatible protocol), stored under `refs/dolt/data` on your git
> remote — separate from `refs/heads/*` where your code lives.
> `.beads/issues.jsonl` is a passive export, not the wire protocol.
>
> See [SYNC_CONCEPTS.md](https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md)
> for the one-screen overview and anti-patterns (don't treat JSONL as the
> source of truth; don't `bd import` during normal operation; don't
> reach for third-party Dolt hosting before trying the default).

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work atomically
bd close <id>         # Complete work
bd dolt push          # Push beads data to remote
```

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts.

Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems, causing the agent to hang indefinitely waiting for y/n input.

**Use these forms instead:**
```bash
# Force overwrite without prompting
cp -f source dest           # NOT: cp source dest
mv -f source dest           # NOT: mv source dest
rm -f file                  # NOT: rm file

# For recursive operations
rm -rf directory            # NOT: rm -r directory
cp -rf source dest          # NOT: cp -r source dest
```

**Other commands that may prompt:**
- `scp` - use `-o BatchMode=yes` for non-interactive
- `ssh` - use `-o BatchMode=yes` to fail instead of prompting
- `apt-get` - use `-y` flag
- `brew` - use `HOMEBREW_NO_AUTO_UPDATE=1` env var

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:970c3bf2 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files
- When working on a feature, commit after each passing test. Avoid large diffs.
- Flag assumptions instead of presenting them as settled.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   bd dolt push
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->
## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
