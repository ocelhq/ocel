# Config resolution & TypeScript execution

Status: done

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

Give the CLI the ability to find and read the user's `ocel.config.ts`. Walking
up from the working directory (tsconfig-style), locate the nearest
`ocel.config.ts`. Transpile/bundle it in-process with the esbuild Go API to the
`.ocel/` folder next to the config, execute the output with the user's `node`,
and capture its emitted configuration as JSON: `projectId` and
`discovery.paths` (default `["ocel"]` when omitted).

If no config is found, or it is unparseable, or `projectId` is missing, print a
message instructing the user to run `ocel init` and exit non-zero.

`.ocel/` is an Ocel-internal build-artifact folder and must be gitignored.

The only hard runtime dependency introduced is that `node` is on PATH; add the
esbuild Go dependency to `ocel/go.mod`.

## Acceptance criteria

- [x] Running from any subdirectory resolves the nearest ancestor `ocel.config.ts`.
- [x] A valid config yields the parsed `projectId` and resolved `discovery.paths` (default `["ocel"]`).
- [x] Missing config, unparseable config, or missing `projectId` prints an `ocel init` instruction and exits non-zero.
- [x] Build artifacts are written under `.ocel/`, and `.ocel/` is gitignored.
- [x] esbuild is vendored via the Go API (no npm esbuild / npx dependency).
- [x] `cd ocel && go build ./...` compiles cleanly.

## Blocked by

None - can start immediately.

## Resolution

Implemented as a new `ocel/internal/projectconfig` package:

- `findConfigFile` walks up from a start directory (tsconfig-style) looking
  for `ocel.config.ts`.
- `Resolve` bundles the found config with the esbuild Go API
  (`github.com/evanw/esbuild`, added to `ocel/go.mod`) into a small wrapper
  entrypoint that imports the config's default export and
  `JSON.stringify`s it, writes the bundle to `.ocel/config.mjs` next to the
  config, executes it with the user's `node` (only hard runtime dependency),
  and parses the resulting JSON into `projectId` + `discovery.paths`
  (defaulting to `["ocel"]`).
- Every failure mode (config not found, bundle/parse/exec failure, missing
  `projectId`) returns an error whose message tells the user to run
  `ocel init`; `ocel dev` now calls `Resolve` up front and exits non-zero on
  error instead of always falling through to the stub server.
- `.ocel` was already covered by the repo's root `.gitignore` (no change
  needed there).
- Covered by unit tests in `projectconfig_test.go` (directory walk-up,
  valid config, default discovery paths, all three error paths, and build
  artifact location); `cd ocel && go build ./...` and `go test ./...` both
  pass.

Not done here (left for follow-up issues per the PRD): actually wiring the
resolved config into discovery/auth/provisioning in `ocel dev` — `dev.go`
resolves the config but the result isn't consumed yet beyond validating it.
