# Tests

| suite         | what it drives                                                       |
| ------------- | -------------------------------------------------------------------- |
| `journeys`    | one example on one target, through the real `ocel` binary, over HTTP |
| `dev`         | the dev-server suites the `dev` target replaces                      |
| `next-compat` | Next.js's own deployment-adapter harness, by workflow dispatch only  |

Unit tests live beside the code they cover, in vitest for TypeScript and `go test`
for Go. The Go provider suites (`TestLive*`) and lifecycle suites (`TestE2E*`) stay
in their provider packages and run from the provider workflows.

## Journeys

A cell is one example on one target, named in `journeys/src/spec.ts`. The harness
starts nothing but the `ocel` binary; bring up what the target needs first. For `dev`
that is postgres, the control-plane schema and the console:

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

For `aws` that is a floci emulator, one per edge, and the endpoint it prints:

```
scripts/floci.sh create ocel-journeys
export AWS_ENDPOINT_URL=<the OCEL_FLOCI_ENDPOINT it printed>
export OCEL_AWS_EDGE=api-gateway
pnpm --filter @ocel-tests/journeys cell --example express --target aws
scripts/floci.sh destroy ocel-journeys
```

The target pins the AWS config and credentials files to empty files of its own, so a
profile in `~/.aws` cannot redirect one service at the endpoint. A bootstrap cannot be
updated on floci (#853), so a second run wants a fresh emulator.

Projects stranded by a run that died are reclaimed per target, and only ones the harness
named:

```
pnpm --filter @ocel-tests/journeys sweep --target aws
```

`--shard <index>/<total>` is accepted and validated; it selects nothing yet.

Real clouds are reached by workflow dispatch only. Nothing here spends a real account.

A cell that is expected to fail is listed in `journeys/src/expectations/<environment>.ts`
with the issue that owns the gap, and un-listed in the PR that fixes it.

Evidence and the run's account land under `journeys/output/`, which is untracked and
uploaded as a workflow artifact.
