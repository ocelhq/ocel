# Tests

| suite      | what it drives                                                            |
| ---------- | ------------------------------------------------------------------------- |
| `journeys` | one example on one target, through the real `ocel` binary, over HTTP       |
| `dev`      | the dev-server suites the `dev` target replaces                            |

Unit tests live beside the code they cover, in vitest for TypeScript and `go test`
for Go. The Go provider suites (`TestLive*`) and lifecycle suites (`TestE2E*`) stay
in their provider packages and run from the provider workflows.

## Journeys

A cell is one example on one target. `OCEL_TARGET` picks the target for the process;
each example has one test file. Legs run in a fixed sequence — up, contract,
redeploy, contract, rollback, contract, destroy — and a target declares the legs it
has. `dev` is complete at up, contract and destroy.

The contract is one table of HTTP rows in `journeys/src/contract.ts`, split into the
suites `health`, `static`, `product` and `probes`. Each example lists the suites it
serves in the spec table, so a hello app runs exactly the rows that apply to it.

### Running one journey locally

The harness starts nothing but the `ocel` binary. Bring up what the target needs
first. For `dev` that is postgres, the control-plane schema and the console:

```
go -C cli build -o bin/ocel ./ocel
docker compose up -d postgres ocel-cloud minio
pnpm --filter @console/db db:push
pnpm --filter @console/web dev
```

Then run one cell:

```
pnpm --filter @ocel-tests/journeys cell --example express --target dev
```

`--shard <index>/<total>` is accepted and validated; it selects nothing yet.

Real clouds are reached by workflow dispatch only. Nothing here spends a real account.

### Expectations and the account

`journeys/src/expectations/<environment>.ts` lists every cell expected to fail,
keyed by `example/app` then the exact test title, with the issue that owns the gap as
the value. A listed test that fails is red and the run stays green; a listed test
that passes fails the run, so a fixed gap is un-listed in the PR that fixes it and
the file only shrinks.

A reporter reconciles the spec table by apps by declared legs by contract rows
against what actually ran. A test that never ran fails, an unlisted failure fails,
and any skip, todo or only fails — a test cannot be quietly disabled. The reporter
writes a markdown table to the run's output directory and appends it to the job
summary in CI.

### Evidence

Every leg writes the binary's stdout and stderr and the deployment under
`journeys/output/<run>/<target>/<example>/<leg>/`. The directory is untracked and is
uploaded as a workflow artifact.
