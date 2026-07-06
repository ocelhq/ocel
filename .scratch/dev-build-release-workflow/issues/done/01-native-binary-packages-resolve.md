# Native binary packages resolve end-to-end

Status: done

## Parent

`.scratch/dev-build-release-workflow/PRD.md`

## What to build

The tracer-bullet slice that makes `packages/ocel`'s existing `optionalDependencies` finally resolve to real, invocable binaries. This cuts through every layer of the packaging path:

- Add `packages/native-lib/*` to the pnpm workspace globs so the per-platform packages are recognized as workspace members.
- Narrow the blanket `packages/native-lib/**` git-ignore rule to only each package's binary output directory, so `package.json` files are tracked while binaries are not.
- Scaffold the 4 native carrier packages (folder convention `cli-<platform>-<arch>`, published name `@ocel/<platform>-<arch>`) for the existing matrix: `darwin-arm64`, `darwin-x64`, `linux-x64`, `win32-x64`. Each `package.json` declares `os`/`cpu` matching Node's platform/arch strings, `files: ["bin"]`, a version matching `packages/ocel`, and deliberately **no `exports` field** (an `exports` field would break `run.js`'s deep-path `require.resolve`).
- Write the shared `build-native.mjs` script: single implementation of the Node platform/arch <-> Go `GOOS`/`GOARCH` mapping and the `go build` invocation. Always `CGO_ENABLED=0`. Supports explicit `--goos`/`--goarch`/`--out` and a `--host` mode (current machine). Sets the executable bit on non-Windows output. Accepts an optional version string for `-ldflags -X .../cli.version=` injection.

End state: run the build script in host mode, the binary lands in the matching `cli-*` package's `bin/`, `pnpm install` resolves the `optionalDependencies`, and invoking the CLI through `packages/ocel`'s `run.js` executes the real Go binary.

## Acceptance criteria

- [x] `pnpm-workspace.yaml` includes `packages/native-lib/*` and `pnpm install` links all 4 native packages
- [x] `.gitignore` tracks each native package's `package.json` but ignores its `bin/` output
- [x] All 4 native `package.json` files exist with correct `@ocel/*` names, `os`/`cpu`, `files`, matching version, and no `exports` field
- [x] `build-native.mjs --host` produces a working binary in the matching native package's `bin/`, with the executable bit set on non-Windows
- [x] `build-native.mjs` accepts explicit `--goos`/`--goarch`/`--out` and always builds with `CGO_ENABLED=0`
- [x] `packages/ocel`'s `run.js` resolves and executes the host-built binary without modification to `run.js`

## Blocked by

None - can start immediately

## Resolution

Verified on a linux-x64 host: `pnpm install` links all 4 workspace packages
(darwin-arm64/darwin-x64/win32-x64 correctly skipped by pnpm's os/cpu
platform filter, linux-x64 symlinked); `node scripts/build-native.mjs --host`
produced a real ELF binary with the executable bit set at
`packages/native-lib/cli-linux-x64/bin/ocel`; explicit `--goos windows
--goarch amd64 --out ... --version 1.2.3` cross-compiled a PE32+ binary with
the version string injected; `node packages/ocel/bin/run.js --version` and
`--help` executed the real Go binary through the unmodified `run.js` deep-path
resolution. `go build ./...` (from `ocel/`) and `pnpm --filter ocel build`
(tsc) both pass. darwin/win32 binaries were not built or run on this host —
only the cross-compile path (`go build` with `GOOS`/`GOARCH` set,
`CGO_ENABLED=0`) was exercised for those targets, not actual execution.
