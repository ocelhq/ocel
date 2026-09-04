# Tests

| suite         | what it drives                                                       |
| ------------- | -------------------------------------------------------------------- |
| `journeys`    | one example on one target, through the real `ocel` binary, over HTTP |
| `next-compat` | Next.js's own deployment-adapter harness, by workflow dispatch only  |

Unit tests live beside the code they cover, in vitest for TypeScript and `go test` for Go.
The Go provider suites (`TestLive*`) stay in the provider packages and, for the image build
and the container dry run, in the CLI; the lifecycle suites (`TestLifecycle*`) stay in the
provider packages alone. Both run from the provider workflows.

## Running one journey locally

A cell is one example on one target, named in `journeys/src/spec.ts`. The harness starts
nothing but the `ocel` binary; bring up what the target needs first. For `dev` that is
postgres, the control-plane schema and the console:

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

For `aws` that is a floci emulator and the endpoint it prints:

```
scripts/floci.sh create ocel-journeys
export AWS_ENDPOINT_URL=http://localhost.localstack.cloud:<the port it printed>
pnpm --filter @ocel-tests/journeys cell --example express --target aws
scripts/floci.sh destroy ocel-journeys
```

One emulator serves every edge. `OCEL_JOURNEY_EDGES` and `OCEL_JOURNEY_COMPUTES` narrow
what the target offers, and `OCEL_JOURNEY_COVERAGE=full` runs every variant rather than a
covering subset.

The host is `localhost.localstack.cloud`, not the `127.0.0.1` the script prints: S3-Control
addresses its endpoint as `<account>.<host>`, and `<account>.127.0.0.1` resolves nowhere,
so a bootstrap against the printed form fails where the named form works (#888). A
bootstrap cannot be updated on floci (#853), so a second run wants a fresh emulator.

For `vps` that is a box the run can reach over SSH, and on a laptop that is an incus VM:

```
go -C cli build -o bin/ocel ./ocel
node scripts/build-native.mjs --host --target provider-vps
scripts/incus.sh create journey
eval "$(scripts/incus.sh info journey)"
export OCEL_VPS_HOST=$OCEL_INCUS_ADDR OCEL_VPS_USER=$OCEL_INCUS_USER OCEL_VPS_IDENTITY_FILE=$OCEL_INCUS_KEY
pnpm --filter @ocel-tests/journeys cell --example express --target vps
```

`scripts/ec2.sh` is the same box on a real EC2 instance, for when the deploy has to face a
public IP and a real network. It spends the AWS account you are authenticated against, so
destroy it when the run ends:

```
scripts/ec2.sh create journey
eval "$(scripts/ec2.sh info journey)"
export OCEL_VPS_HOST=$OCEL_EC2_ADDR OCEL_VPS_USER=$OCEL_EC2_USER OCEL_VPS_IDENTITY_FILE=$OCEL_EC2_KEY
pnpm --filter @ocel-tests/journeys cell --example express --target vps
scripts/ec2.sh destroy journey
```

`pnpm --filter @ocel-tests/journeys sweep --target <target>` reclaims what a run that died
left behind, and only projects the harness named.

`--shard <index>/<total>` is accepted and validated by `cell`; it selects nothing yet.

A pull request runs one member of each example group, plus every member whose directory the
diff touches. A full run — workflow dispatch, or the `journey:real` label — runs every
member. To reproduce a pull request's pick on a laptop:

```
OCEL_JOURNEY_SEED=<pull request number> OCEL_JOURNEY_TOUCHED=<dir,dir> \
  pnpm --filter @ocel-tests/journeys journey
```

Real clouds are reached by workflow dispatch, or by putting the `journey:real` label on a
pull request — one shot, the label comes off again as the run starts. From here only
`scripts/ec2.sh` spends a real account.

Every known gap is one entry in `journeys/src/expectations/gaps.ts`: a slug, a reason,
the issue that owns it when one does, and the environments, edges, cells and tests it
affects. A test can sit under several gaps and a gap under many tests; the run resolves the
list for its own environment. A gap is un-listed in the pull request that fixes it. The
account is exact in both directions: a listed test that passes fails the run, an unlisted
test that fails fails the run, and a skipped, `todo` or `only` test fails the run whatever
the list says.

Evidence and the run's account land under `journeys/output/`, which is untracked and
uploaded as a workflow artifact.

## Running next-compat locally

The pure half — the manifest and result comparisons, not the Lambda — runs anywhere:

```
pnpm --filter @ocel-tests/next-compat test
```

Driving the harness itself needs a Next.js checkout at the ref the adapter pins, an `ocel`
sidecar to install into each temp app, and credentials for a real AWS and Cloudflare
account, because every case deploys. One suite at a time:

```
RUN_ID=local-$USER OUT_DIR=/tmp/next-compat NEXT_DIR=<next.js checkout> \
  OCEL_E2E_SIDECAR_DIR=<sidecar dir> \
  tests/next-compat/run-one.sh test/e2e/app-dir/app-basepath/index.test.ts
```

`next-compat/README.md` is the source of truth for building the sidecar, recording a
baseline and reclaiming a stranded project.

## One-time human setup

Both real lanes need a human to prepare an account once.

The `aws` lane assumes a role over GitHub OIDC — no access key is stored — and hard-fails
if the session or the Cloudflare token resolves to an account other than the one named. The
role's `MaxSessionDuration` must be at least 14400 seconds, the duration the journey job
mints, or the assume fails outright.

| name                                 | kind   | what it holds                                               |
| ------------------------------------ | ------ | ----------------------------------------------------------- |
| `E2E_AWS_ROLE_ARN`                   | secret | the role every AWS-touching job assumes                      |
| `E2E_AWS_REGION`                     | var    | the region the session is minted in and the apps deploy into |
| `E2E_CLOUDFLARE_API_TOKEN`           | secret | the Cloudflare API token the edge deploys with               |
| `E2E_CLOUDFLARE_ACCOUNT_ID`          | secret | the Cloudflare account passed to the provider                |
| `E2E_EXPECTED_AWS_ACCOUNT_ID`        | secret | the only AWS account the guard lets a run touch              |
| `E2E_EXPECTED_CLOUDFLARE_ACCOUNT_ID` | secret | the only Cloudflare account the guard lets a run touch       |
| `E2E_PREVIEW_DOMAIN`                 | var    | the zone a dispatched run may take hostnames under           |

The `vps` lane points at an incus VM on a pull request, and on a real run brings up a
throwaway EC2 box with `scripts/ec2.sh` under the same role and account guard as the `aws`
lane, so it needs no secrets of its own. The job destroys the box when it ends, whether the
journey passed or not, and the nightly `Sweep` workflow reclaims any box older than three
hours that a run which died left behind.
