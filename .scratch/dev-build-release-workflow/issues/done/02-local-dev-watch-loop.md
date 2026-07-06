# Local dev watch loop

Status: done

## Parent

`.scratch/dev-build-release-workflow/PRD.md`

## What to build

A watch-and-rebuild loop so a contributor editing the Go CLI sees the change reflected through the TypeScript SDK immediately, with no manual copy or reinstall.

- Add `air` (github.com/air-verse/air) as a `go.mod` tool dependency using the existing `tool` directive convention (same mechanism as `protoc-gen-go`/`protoc-gen-connect-go`), invoked via `go tool air`.
- Add `ocel/.air.toml` that watches `**/*.go` and, on change, shells out to the shared `build-native.mjs --host` with an output path targeting the current host's matching `packages/native-lib/cli-<platform>-<arch>/bin/ocel[.exe]`. Build-only: no post-build run step.
- Add a root `package.json` `dev:cli` script that starts the watcher (`cd ocel && go tool air`).

Because pnpm symlinks workspace packages, the freshly rebuilt binary is immediately visible to `packages/ocel` without reinstalling.

## Acceptance criteria

- [x] `air` is declared in `ocel/go.mod`'s tool directive and runnable as `go tool air` (no global install needed)
- [x] `ocel/.air.toml` watches Go sources and rebuilds via `build-native.mjs --host` into the correct host native package `bin/`
- [x] Air performs build-only (does not auto-execute the CLI after building)
- [x] `pnpm dev:cli` starts the watch loop from the repo root
- [x] Editing a `.go` file triggers a rebuild whose output is immediately runnable through `packages/ocel`'s `run.js`

## Blocked by

- `.scratch/dev-build-release-workflow/issues/01-native-binary-packages-resolve.md` (done)

## Resolution notes

Air has no built-in "build-only" mode — it always runs something after a
successful build. Its default `bin` (`./tmp/main`) is baked in via a `mergo`
merge with `Overwrite: true` that skips zero-value overrides, so setting
`bin = ""` in the TOML is silently ignored and falls back to the default,
producing a confusing "tmp/main: not found" error every rebuild. Fixed by
setting `entrypoint = ["true"]` (a real no-op command, resolved once via
`exec.LookPath` and cached) instead — this also avoids the `bin` field's
"deprecated, use entrypoint" warning. Added a `[build.windows]` override
(`entrypoint = ["cmd", "/c", "exit 0"]`) for parity, though only the Linux
path was actually exercised (see below).

Verified on linux-x64 host: `go tool air` builds without a global install;
`pnpm dev:cli` starts the loop from repo root; editing
`ocel/internal/cli/root.go` while the loop was running triggered a rebuild
(air logged "internal/cli/root.go has changed" and rebuilt
`packages/native-lib/cli-linux-x64/bin/ocel`) with no CLI auto-execution;
`node packages/ocel/bin/run.js --version` immediately picked up the fresh
binary with no reinstall. `go build ./...` and `pnpm --filter ocel build`
both pass.

Not verified: darwin/win32 hosts (none available) — the `[build.windows]`
entrypoint override is unexercised.
