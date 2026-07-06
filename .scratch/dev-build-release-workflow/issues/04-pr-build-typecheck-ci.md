# PR-time build/typecheck CI

Status: ready-for-agent

## Parent

`.scratch/dev-build-release-workflow/PRD.md`

## What to build

A fast pull-request feedback workflow that catches compile and type errors before merge, without touching cross-compilation or publishing.

- Add `.github/workflows/build.yml` triggered on `pull_request` and `workflow_dispatch`.
- Single `ubuntu-latest` job that runs a host-only `go build ./...` (scoped so it works with the `go.work` setup — run from inside `ocel/` or use `./ocel/...`) plus `pnpm --filter ocel build` (the `tsc` typecheck).
- No cross-compile matrix, no artifacts, no publish.

## Acceptance criteria

- [ ] `.github/workflows/build.yml` triggers on `pull_request` and `workflow_dispatch`
- [ ] Job runs on a single `ubuntu-latest` runner
- [ ] Workflow runs a host-only `go build` for the `ocel` module and `pnpm --filter ocel build`
- [ ] Workflow does not cross-compile, upload artifacts, or publish
- [ ] Sets up the pinned pnpm version and a Go toolchain before building

## Blocked by

None - can start immediately
