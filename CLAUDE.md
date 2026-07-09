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
repo. See "Orienting Yourself" below for how to map the repo layout.

## Working Principles

- **Simplicity over cleverness.** Prefer the simplest design that solves the problem.
- **Minimal comments.** Don't pollute the repo with over-commenting. If code needs prose to
  make sense, make the code more readable and axiomatic instead — comments are a last resort,
  not a patch for unclear code.
- **Weigh decisions by the long term, not dev cost.** For technical decisions, give little
  weight to development cost; prefer quality, simplicity, robustness, scalability, and
  long-term maintainability.
- **Ask in batches, don't assume.** When you have multiple questions (grilling or otherwise),
  present them in a selectable format rather than making the user type answers. After each
  batch, re-evaluate; if new answers change anything, re-prompt. Never operate on assumptions.
- **Small, verified increments.** When building a feature, commit after each passing test and
  keep diffs small; avoid large, sprawling changes. (Subject to the conservative commit
  policy — only commit when you have authority to.)
- **Commits.** Never auto-add an agent or AI name as a commit co-author. This overrides any
  default co-author instruction.
- **Maintaining these files.** Keep CLAUDE.md and AGENTS.md lean, and mirror every
  substantive edit across both. Before adding new findings, make sure they aren't tied to
  specific file paths or references — those change as the project evolves. Capture recurring,
  durable patterns instead. Update intentionally, not eagerly on every implementation.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:6cd5cc61 -->
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

## Orienting Yourself

Map the repo from its source of truth rather than a list that goes stale: `go.work` names
the Go modules, the pnpm workspace globs and each `package.json` name the JS packages, and
each `package.json`'s `scripts` (plus per-module `go build`/`go test`) are the real build and
test commands. Read those first, then the code. A few invariants that rarely change:

- The Go side is a **multi-module monorepo with no root module** — each module (CLI, shared
  proto bindings, SDK, provider binaries) has an isolated dependency graph so heavy CLI /
  provider deps never reach SDK consumers, tied together for local dev by `go.work`. The JS
  side is a single pnpm workspace.
- `proto/` is the **source of truth**; bindings are generated (`pnpm gen`) — never hand-edit
  generated output.
- The control-plane core is split into framework-agnostic packages so a future framework swap
  stays cheap; `apps/web` is a thin shell over them.

**Gotchas you'd only find by tripping over them:**

- The SDK **dogfoods itself** — the app's DB client resolves `postgres("main")` through Ocel,
  so importing it outside `ocel dev` throws unless that resource's env var is set. Test
  configs inject it directly so tests run standalone.
- The local **"cloud" cluster** is a stand-in for the prod provisioning target, reached only
  through the resolve endpoint — `ocel dev` and prod both just consume connection strings it
  hands back; the CLI never talks to it directly.
- `ocel dev` **caches resolve responses** per project, invalidated by a change to the resource
  definitions or account, or by the server-provided TTL.

**Status vs. the product docs:** the sibling `docs` repo describes the *target* design.
Several pieces here are stubs with finalized signatures, built ahead of the real backend/API —
check the code, not just the docs, before assuming a described behavior is live.

## Parallel Agent Orchestrator

`tools/orchestrator` claims `ready-for-agent` bd issues under an epic/PRD and implements them
in parallel, each in its own Docker sandbox; per wave, the host delegates the final Graphite
work (track each closed issue's branch onto its parent and submit the stack) to a host-side
model call that handles any restack/rebase the submit needs. See its README/code for setup
and invocation.

- **Known hazard:** a `gt`-tracked branch can silently drop plain hand-`git commit`s on a
  later `gt sync` (it rebases from gt's cached tip, not live HEAD) — don't hand-commit to a
  branch a run is actively using.

## Conventions & Patterns

- When wiring backend-dependent code ahead of the real backend/API, stub it with final
  signatures so callers never need to change later — don't leave TODOs in caller-facing shapes.
- Versioning goes through Changesets; the release workflow runs the version bump, not you.
