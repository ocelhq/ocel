# Tests

| suite      | what it drives                                                      |
| ---------- | ------------------------------------------------------------------- |
| `journeys` | one example on one target, through the real `ocel` binary, over HTTP |
| `dev`      | the dev-server suites the `dev` target replaces                      |

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

`--shard <index>/<total>` is accepted and validated; it selects nothing yet.

Real clouds are reached by workflow dispatch only. Nothing here spends a real account.

A cell that is expected to fail is listed in `journeys/src/expectations/<environment>.ts`
with the issue that owns the gap, and un-listed in the PR that fixes it.

Evidence and the run's account land under `journeys/output/`, which is untracked and
uploaded as a workflow artifact.
