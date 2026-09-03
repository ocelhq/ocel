# tests

The apps in [examples](../examples) are the fixtures. Every suite here deploys
one of them and checks what came up.

| Suite | What it runs | Where |
| --- | --- | --- |
| journey | One example, one target: bring it up, hit every route, redeploy with a changed env var, roll back, destroy. | `journey/` |
| next-compat | Next.js's own deployment test suite against the `next` example. Vendored, not edited. | `next-compat/` |

Unit tests live next to the code they test. Provider and lifecycle tests are Go,
under `platform/<vendor>/provider`.

## Run a journey locally

```sh
pnpm install
pnpm --filter ocel build
TARGET=dev EXAMPLE=express pnpm --filter @ocel-tests/journey test
```

Targets: `dev` (the console, started for you), `aws` (floci in Docker),
`vps` (an incus VM). The harness starts the emulator for `aws` and `vps`; you
need Docker or incus installed. Real clouds run from a workflow dispatch, never
from a laptop.

A cell that fails on purpose is listed in `journey/expectations/<target>.json`
with a link to the issue that will flip it green. The run refuses to pass if any
cell was skipped rather than run.

## Run next-compat locally

```sh
pnpm --filter @ocel-tests/next-compat test
```
