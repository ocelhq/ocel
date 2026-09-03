# tests

The apps in [examples](../examples) are the fixtures. Every suite here deploys
one of them and checks what came up.

| Suite | What it runs | Where |
| --- | --- | --- |
| journeys | One example, one target: bring it up, hit every route, redeploy with a changed env var, roll back, destroy. | `journeys/` |
| next-compat | Next.js's own deployment test suite against the `next` example. Vendored, not edited. | `next-compat/` |

Unit tests live next to the code they test. Provider and lifecycle tests are Go,
under `platform/<vendor>/provider`.

## Run one journey locally

```sh
pnpm install
pnpm --filter ocel build
pnpm -C tests/journeys cell dev express
```

Targets: `dev` (the console, started for you), `aws` (floci in Docker), `vps`
(an incus VM). Start the emulator first; the cell tells you the command if it
is missing:

```sh
scripts/floci.sh create journeys
scripts/incus.sh create journeys
```

Real clouds run from a workflow dispatch, never from a laptop.

A cell that fails on purpose is listed in `journeys/expectations/<target>.json`
with a link to the issue that will flip it green. The run refuses to pass if any
cell was skipped rather than run. Output from every cell lands in
`journeys/.out/`.

## Run next-compat locally

```sh
pnpm -C tests/next-compat test
```
