# Decisions proposed alongside the READMEs

## examples/README.md

Keep it, cut it to an index: one line per example and a link. The ladder
narrative moves into `with-transforms`'s README, the one rung that needs the
story to make sense. `with-sst` and `with-pulumi` keep their "rung three"
opener and link to `with-transforms` instead of the index.

Proposed body:

    # Examples

    Every example deploys with `ocel deploy`. The test suite uses them as fixtures.

    - [hello-express](./hello-express), [hello-next](./hello-next) — an app that declares nothing.
    - [express](./express), [hono](./hono), [fastify](./fastify), [next](./next) — one full app per framework, using the SDK.
    - [with-transforms](./with-transforms) — the same app, with the provisioning shaped by rules in the repo.
    - [with-sst](./with-sst), [with-pulumi](./with-pulumi) — your own IaC owns the database and network; ocel deploys the app into it.
    - [workspace](./workspace) — three apps under one project.

## scripts/

Stays: `floci.sh`, `incus.sh`, `assert-ran.sh`, `act.sh`, `gobuildcache-setup.sh`,
`build-native.mjs`, `verify-next-bundle.mjs`. The two emulator scripts and the
accounting script serve the Go provider and lifecycle workflows, which the map
keeps in Go. The journey harness does not start emulators: a developer runs
`floci.sh`/`incus.sh` before a cell, the workflow runs them before the matrix,
and a cell fails fast naming the command when its emulator is absent.
`assert-ran.sh` reads `go test -json`; the vitest account is the harness's own
reporter and does not reuse it.

Goes: `scripts/e2e-node`, `scripts/e2e-sst` (settled on the map),
`scripts/e2e-next` (moves to `tests/next-compat`). `act.sh`'s workflow list
follows the renames decided in the workflow ticket.
