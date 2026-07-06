# Local dev watch loop

Status: ready-for-agent

## Parent

`.scratch/dev-build-release-workflow/PRD.md`

## What to build

A watch-and-rebuild loop so a contributor editing the Go CLI sees the change reflected through the TypeScript SDK immediately, with no manual copy or reinstall.

- Add `air` (github.com/air-verse/air) as a `go.mod` tool dependency using the existing `tool` directive convention (same mechanism as `protoc-gen-go`/`protoc-gen-connect-go`), invoked via `go tool air`.
- Add `ocel/.air.toml` that watches `**/*.go` and, on change, shells out to the shared `build-native.mjs --host` with an output path targeting the current host's matching `packages/native-lib/cli-<platform>-<arch>/bin/ocel[.exe]`. Build-only: no post-build run step.
- Add a root `package.json` `dev:cli` script that starts the watcher (`cd ocel && go tool air`).

Because pnpm symlinks workspace packages, the freshly rebuilt binary is immediately visible to `packages/ocel` without reinstalling.

## Acceptance criteria

- [ ] `air` is declared in `ocel/go.mod`'s tool directive and runnable as `go tool air` (no global install needed)
- [ ] `ocel/.air.toml` watches Go sources and rebuilds via `build-native.mjs --host` into the correct host native package `bin/`
- [ ] Air performs build-only (does not auto-execute the CLI after building)
- [ ] `pnpm dev:cli` starts the watch loop from the repo root
- [ ] Editing a `.go` file triggers a rebuild whose output is immediately runnable through `packages/ocel`'s `run.js`

## Blocked by

- `.scratch/dev-build-release-workflow/issues/01-native-binary-packages-resolve.md`
