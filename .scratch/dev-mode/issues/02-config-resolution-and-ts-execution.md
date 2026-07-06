# Config resolution & TypeScript execution

Status: in-progress

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

- [ ] Running from any subdirectory resolves the nearest ancestor `ocel.config.ts`.
- [ ] A valid config yields the parsed `projectId` and resolved `discovery.paths` (default `["ocel"]`).
- [ ] Missing config, unparseable config, or missing `projectId` prints an `ocel init` instruction and exits non-zero.
- [ ] Build artifacts are written under `.ocel/`, and `.ocel/` is gitignored.
- [ ] esbuild is vendored via the Go API (no npm esbuild / npx dependency).
- [ ] `cd ocel && go build ./...` compiles cleanly.

## Blocked by

None - can start immediately.
