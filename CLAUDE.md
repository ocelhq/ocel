# Project Instructions for AI Agents

This file provides instructions and context for AI coding agents working on this project.

## What This Is

Ocel — "Platform as a Framework." An SDK that turns cloud infrastructure into function
calls: call `postgres("main")` in app code and that call *is* the provisioning step.
`ocel dev` connects it to a real, running instance instantly (no Docker, no emulators);
on deploy, the same code compiles to real infrastructure-as-code and lands in the
user's own AWS/GCP account, not a vendor-owned black box. Full product docs (philosophy,
comparisons, PRDs) live in the sibling `docs` repo — read there before making
product-framing claims; this file only covers what an agent needs to build in *this*
repo. See "Architecture Overview" below for the repo layout.

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

## Agent skills

### Issue tracker

Issues live in the local beads (`bd`) tracker, not GitHub Issues — external PRs are not a triage surface. See `docs/agents/issue-tracker.md`.

### Generating issues from a plan/PRD

When turning a plan or PRD into a batch of bd issues, analyze the resulting set and
build a dependency graph before filing — don't just create a flat list. For each issue,
determine whether it blocks or is blocked by any other open issue.

Issue B is blocked by issue A if:
- B requires code or infrastructure that A introduces
- B and A modify overlapping files or modules, making concurrent work likely to produce
  merge conflicts
- B's requirements depend on a decision or API shape that A will establish

Wire discovered dependencies with `bd dep add <B> <A>` so `bd ready`/`bd blocked` reflect
them. An issue is unblocked if it has zero blocking dependencies on other open issues.

### Triage labels

Default label strings (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`), applied via `bd label add`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.

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
- `packages/{db,auth,resources,api}` — the control plane's framework-agnostic core,
  extracted from `apps/web` (epic ocelhq-z7j) so a future framework swap stays cheap:
  `@repo/db` (Drizzle client + schema), `@repo/auth` (better-auth config —
  email/password, GitHub OAuth, device-authorization + bearer plugins; import
  `@repo/auth/next` for the Next-cookies variant), `@repo/resources` (the app's own
  Ocel resource declarations, e.g. `postgres("main")`), and `@repo/api`
  (framework-agnostic `Request → Response` route handlers). They all export TS source
  directly (`"exports": "./src/index.ts"`) — consumers transpile/bundle them.
- `apps/web` — Next.js control plane, now a thin shell: `app/api/**/route.ts` files
  just re-export `@repo/api` handlers. `scripts/local-api-server.ts` is a local-dev
  Bun harness mounting those same handlers plus dev-only
  `/dev/{project-config,provision}` endpoints; the hidden `ocel dev --local-harness`
  flag spawns it (via `ocel/internal/localharness`) so the provisioning handshake can
  resolve against it and tear it down before the app command starts — instead of
  deadlocking on the control plane it is itself starting.
- `proto/` — protobuf source of truth (buf), codegen'd into `ocel/pkg/proto` (Go) and
  `packages/ocel/src/gen` (TS) via `pnpm gen`. Edit `.proto` files, then regenerate —
  never hand-edit generated output.

**Status vs. the product docs:** the `docs` repo describes the target design (e.g. real
provisioning, monorepo leader/follower dev mode). Several pieces here are still
stubs with finalized signatures — e.g. `ocel init` and `internal/provision` — built
ahead of the real backend/API. Check the code, not just the docs, before assuming a
described behavior is live.

## Parallel Agent Orchestrator

`tools/orchestrator` (`pnpm --filter @ocel/orchestrator orchestrate <parent-issue-id>`)
claims `ready-for-agent` bd issues under an epic/PRD and implements them in parallel,
each in its own Docker sandbox via [sandcastle](https://github.com/mattpocock/sandcastle)
(`.sandcastle/Dockerfile`, requires `CLAUDE_CODE_OAUTH_TOKEN` in `.sandcastle/.env` — run
`claude setup-token` to get one). Agents never hold a GitHub token; the host tracks and
submits each closed issue's branch with Graphite (`gt`) once the sandbox run finishes.

- A dependent issue's branch stacks on its last-unmerged bd blocker's branch (real
  Graphite stack); an issue with no unmerged blockers is a sibling off the run's feature
  branch. `gt sync` runs at each claim-wave boundary to restack once a blocker's PR merges.
- Requires a one-time `gt auth` + `gt repo init --trunk main` per machine.
- **Known hazard:** once the run's feature branch is `gt track`ed, a later `gt sync` can
  silently drop plain `git commit`s made on it since gt's last look (rebases from gt's
  cached tip, not live HEAD) — don't hand-commit to that branch while a run against it is
  in progress. See the comment above `gtTrack(baseBranch, ...)` in `orchestrator.ts`.
- Supersedes the old host-executed `scripts/orchestrator.mjs` (unsandboxed
  `claude -p --permission-mode bypassPermissions`), removed in favor of this.

## Conventions & Patterns

- Go workspace (`go.work`) covers `./ocel` only; JS side is the pnpm workspace.
- Versioning goes through Changesets (`.changeset/`); `pnpm ci:version` is run by the
  release workflow, not by hand.
- When wiring backend-dependent Go code ahead of the real backend/API, stub it with
  final signatures (see `internal/provision`'s doc comment) so callers never need to
  change later — don't leave TODOs in caller-facing shapes.
- If you discover context while working a feature that would help downstream work or
  other agents — a gotcha, a convention, a stub's real status, a doc that turned out to
  live elsewhere — fold it into this file and `AGENTS.md` before finishing, not just
  into a commit message or PR description.
