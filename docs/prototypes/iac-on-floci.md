# `pulumi up` and `sst deploy` against floci, and Lambda-to-RDS reachability

Timeboxed prototype for #848 (map #830). Throwaway branch `prototype/iac-on-floci`.
Run on 2026-09-03, single machine, floci `floci/floci:latest` (2.0.1) on a fixed
`127.0.0.1:4566`.

## Results

| Step                                        | Result | Wall clock |
| ------------------------------------------- | ------ | ---------- |
| `ocel doctor` (with-pulumi)                  | green  | 0.1s       |
| `ocel bootstrap production --yes` (first)    | green  | <1s        |
| `ocel bootstrap production --yes` (re-run)   | RED    | <1s        |
| `pulumi up` — example as written             | RED    | 136s       |
| `pulumi up` — AWS resources only, links cut  | green  | 91s        |
| `ocel link set` x2 + `ocel link ls`          | green  | <1s        |
| `ocel deploy` (with-pulumi)                  | RED    | 5s         |
| `sst deploy --stage floci` — AWS resources   | green  | 432s       |
| `sst deploy --stage floci` — `@ocel/sst` link| RED    | (same run) |
| `ocel deploy` (with-sst)                     | not attempted | — |
| Lambda -> RDS TCP + SSLRequest               | green  | 2ms in-function |
| Lambda -> RDS `INSERT`/`SELECT` via `pg`     | green  | <1s        |
| Lambda VPC placement enforced by floci       | RED (not enforced) | — |
| `pulumi destroy` / `sst remove`              | green  | see teardown |

Nothing here required a change to ocel, the CLI or the provider. Two of the four
reds are in ocel's own IaC integration packages and are not floci-specific.

## Environment that worked

floci on a fixed port so a committed Pulumi stack file can name it:

```
docker run -d --name floci-proto -p 127.0.0.1:4566:4566 \
  -e FLOCI_SERVICES_CLOUDFORMATION_ALLOW_STUB_UNSUPPORTED_RESOURCE_TYPES=false \
  -v /var/run/docker.sock:/var/run/docker.sock floci/floci:latest
```

`scripts/floci.sh create` cannot pass that env var, hence the raw `docker run`.
The var had no observable effect — CloudFront types were still stubbed (see red 4).

Every AWS-talking process ran under:

```
AWS_ENDPOINT_URL=http://127.0.0.1:4566
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
AWS_REGION=us-east-1
AWS_DEFAULT_REGION=us-east-1
AWS_CONFIG_FILE=<empty file>
AWS_SHARED_CREDENTIALS_FILE=<empty file>
XDG_CONFIG_HOME=<scratch>
XDG_CACHE_HOME=<scratch>
PULUMI_BACKEND_URL=file://<scratch>
PULUMI_CONFIG_PASSPHRASE=prototype
PULUMI_HOME=<scratch>
OCEL_ACCESS_TOKEN unset
```

The host `aws` is CLI v1 (`aws-cli/1.45.46`), which ignores `AWS_ENDPOINT_URL`
entirely — every probe below passes `--endpoint-url http://127.0.0.1:4566`
explicitly. The Go SDK v2 (ocel) and the Pulumi/SST AWS providers honour their
own config and needed no such flag.

Pulumi came from `mise install pulumi@3.260.0`; the binary is at
`~/.local/share/mise/installs/pulumi/3.260.0/pulumi/pulumi` (the mise shim
refuses to run without a global version set).

The prebuilt CLI and provider binaries were copied into the worktree from the
main checkout (`packages/native-lib/{cli,provider-aws}-linux-x64/bin/`) and each
example needed a hand-made symlink

```
examples/<name>/node_modules/@ocel/provider-aws-linux-x64 -> ../../../../packages/native-lib/provider-aws-linux-x64
```

because `cli/internal/provider/resolve-provider.cjs` resolves the platform
package from `<project>/.ocel/`, and pnpm's strict layout only nests it under
`packages/provider-aws/node_modules`. Not a floci matter, but it blocks any
example-in-workspace run — see the issue list.

`ocel deploy` refuses without `domains.production`, so both examples gained
`domains: { production: ["<slug>.floci.test"] }` and a hosted zone was created
by hand:

```
aws --endpoint-url http://127.0.0.1:4566 route53 create-hosted-zone \
  --name floci.test --caller-reference proto1
```

Both examples also lost `edge: cloudflare()`; the provider's default CloudFront
edge is picked up and `ocel doctor` reports `provider default edge`.

## Reds

### 1. `ocel bootstrap production` is not re-runnable on floci — floci gap

```
internal: plan the ocel-bootstrap update: operation error CloudFormation:
DescribeChangeSet, https response error StatusCode: 400, api error
ValidationError: Stack with id null does not exist
```

The first bootstrap creates all 16 resources and exits 0. Every later run — in
either example, since the bootstrap is account-wide — fails. Isolated by probe:
floci's `CreateChangeSet` with `ChangeSetType=UPDATE` returns a change-set ARN,
and `DescribeChangeSet` given that ARN alone answers `Stack with id null does
not exist`; the same call with `--change-set-name probe2 --stack-name
ocel-bootstrap` succeeds. Real CloudFormation accepts the ARN on its own.
`platform/aws/provider/bootstrap/bootstrap.go:756` passes only
`ChangeSetName: aws.String(id)`, which is correct against AWS.

Issue to file: **floci's `DescribeChangeSet` does not accept a change-set ARN
without a stack name, so a second `ocel bootstrap` fails**

A second floci defect turned up in the same probe and is worth the same issue or
a sibling: an `UPDATE` change set built with `--use-previous-template` reports
`Action: Remove` for every resource in the stack, i.e. floci does not diff the
previous template.

### 2. `pulumi up` of examples/with-pulumi as written — ocel gap in `@ocel/pulumi`

```
error: Error serializing '() => provider': index.js(50,43)
  captured variable 'provider' which indirectly referenced
    function 'create': resource.js(20,20): which referenced
      '(inputs) => runLink(["set", "--owner ...': resource.js(18,16): which referenced
        function 'runLink': cli.js(5,23): which referenced
          function 'ocelCommand': cli.js(45,20): which referenced
            function 'createRequire': which referenced
              function 'fileURLToPath': which captured
                'ERR_INVALID_ARG_TYPE' ... 'RegExpPrototypeExec', a function
                defined at 'bound call': which could not be serialized because
                it was a native code function
```

Fails during preview, so nothing reaches the emulator; it is not a floci
failure and would fail identically against real AWS. `packages/pulumi/src/resource.ts:172`
hands a plain object literal to `dynamic.Resource`, and Pulumi serialises that
provider's closure. The closure reaches `packages/pulumi/src/cli.ts:71`
`createRequire` (imported ESM-style from `node:module`), which the serialiser
follows into Node internals instead of emitting a `require("module")` reference.
Reproduced identically on Node 26.7.0 and Node 24.19.0, so it is not a Node
version artefact. `scripts/` carries e2e harnesses for sst, next and node but
none for pulumi, which fits: this path has never been run end to end.

Issue to file: **`@ocel/pulumi`'s dynamic provider cannot be serialised by
Pulumi, so `pulumi up` fails at preview**

With the two `link.*` calls removed (and the same values exported instead),
`pulumi up` is green in 91s and creates all 12 resources on floci:
`aws.getAvailabilityZonesOutput` returns `us-east-1a`/`us-east-1b`,
`aws.rds.InstanceType.T4G_Micro` is accepted, gateway VPC endpoints for s3 and
dynamodb create in 5s each, and the RDS instance takes 82s.

### 3. `sst deploy` — ocel gap in `@ocel/sst`

```
Error: @ocel/sst links from inside sst.config.ts, where SST provides $util;
it found no $util here
    at host (packages/sst/dist/resource.js:151:15)
    at Object.postgres (packages/sst/dist/resource.js:61:18)
    at run (examples/with-sst/sst.config.ts:85:10)
```

Every AWS resource SST declares was created on floci first — VPC, both public
and both private subnets, route tables and associations, internet gateway,
default security group, cloudmap namespace, the three VPC endpoints declared in
the example, the RDS subnet group, parameter group, Secrets Manager secret and
version, and `OrdersInstance aws:rds:Instance` in 82.6s. The run then failed on
the ocel link, exit 1 after 432s (most of which was downloading SST's own
pulumi 3.210.0, bun 1.2.1 and five providers).

`packages/sst/src/resource.ts:269` reads `globalThis.$util`. SST 3.19.3 supplies
`$util` as an esbuild-injected module binding — `.sst/platform/src/shim/run.js`
exports `util as "$util"` — not as a property of `globalThis`, so the lookup is
always undefined. `examples/with-sst/package.json` pins `sst: ^3.17.10` and 3.19.3
resolved; whether 3.17 set a global and 3.19 stopped is unverified.

Issue to file: **`@ocel/sst` reads `$util` off `globalThis`, which SST 3.19 does
not set, so every `link.*` call throws during `sst deploy`**

The SST-side endpoint configuration this prototype added to `sst.config.ts` is
green and needs no follow-up: SST forwards `providers.aws` verbatim, and the
generated `pulumi up` command line carries `--config aws:endpoints=[{...}]`,
`aws:accessKey`, `aws:secretKey` and `aws:region`. SST's own bootstrap (asset and
state buckets, `sst-asset` ECR repo, `/sst/bootstrap` and
`/sst/passphrase/with-sst/floci` SSM params) went through the default credential
chain against floci with no extra configuration.

### 4. `ocel deploy` cannot get past the edge on floci — floci gap, belongs to #847

```
⚠ Could not read who serves with-pulumi.floci.test: read the key value store the
  "cloudfront" edge routes production with: operation error CloudFront:
  DescribeKeyValueStore, StatusCode: 404, api error UnknownError: UnknownError

✗ Failed
  internal: create the distribution "ocel--with-pulumi--production": operation
  error CloudFront: CreateDistribution, StatusCode: 404,
  NoSuchResponseHeadersPolicy: The specified response headers policy does not exist.
```

Build and Provisioning and Uploading are green; the Edge stage is where it dies,
and no Lambda function is ever created (`lambda list-functions` returns `[]`
after the run). The cause is that floci's CloudFormation **stubs** the CloudFront
resource types: `describe-stack-resources --stack-name ocel-bootstrap-cloudfront-edge`
reports `CREATE_COMPLETE` for `EdgeHeadersPolicy` with physical id
`EdgeHeadersPolicy-36badbaa`, while `cloudfront list-response-headers-policies
--type custom` returns zero items — only the five managed policies exist. The
`FLOCI_SERVICES_CLOUDFORMATION_ALLOW_STUB_UNSUPPORTED_RESOURCE_TYPES=false` env
var was set on the container and did not change this.

Issue to file: **floci's CloudFormation fabricates CloudFront resources it never
creates, so the ocel edge bootstrap looks applied and `CreateDistribution` then
fails** — likely a duplicate of, or a note on, #847.

A smaller circularity was hit on the way and is worth recording: with no
`domains.production` declared, `ocel deploy` says to run `ocel domain add`, and
`ocel domain add` says `this project has no production deploys yet; run
'ocel deploy' first`. Declaring the domain in the config broke the loop, but the
two messages together point at nothing.

### 5. floci does not enforce VPC or security-group isolation — floci gap

A control probe: the identical function created with **no** `--vpc-config` at all
connected to the same RDS instance and ran the same `INSERT`/`SELECT`. Placement
is recorded faithfully by floci (`get-function-configuration` returns the subnets,
security group and `VpcId`) but is not enforced.

Issue to file: none — this is documented emulator behaviour, but it caps what
any floci suite can claim. A floci run can prove ocel *asks* for the right
subnets and security groups; it can never prove the resulting Lambda would reach
a real private RDS, nor catch a placement bug that would break in AWS.

## Lambda -> RDS reachability: green

Because red 4 stops `ocel deploy` before any function exists, the HTTP path
(`POST /orders`, `GET /orders` against a Function URL or CloudFront hostname)
was never exercised. **This is the fallback path, not the real one.** Two
functions were created directly against floci in the subnets and security group
the `network` link published, and invoked with `aws lambda invoke`.

Probe 1 — raw TCP plus a postgres `SSLRequest` (`nodejs22.x`, no dependencies):

```
{"ok":true,"host":"172.17.0.3","port":7001,"sslReply":"S","ms":2}
```

Probe 2 — the app's actual queries through `pg@8`, against the `orders` table
created beforehand with `psql` from the host:

```
{"ok":true,
 "inserted":{"id":1,"sku":"from-lambda","placed_at":"2026-09-03T07:45:46.253Z"},
 "rows":[{"id":1,"sku":"from-lambda"}]}
```

So a floci Lambda container can open TCP to, authenticate against and query the
floci-managed postgres container. Nothing in floci's Lambda-to-RDS path blocks
the app. Read that together with red 5: it says floci will not get in the way,
not that the placement is right.

Function configuration as floci stored it:

```
"VpcConfig": {"SubnetIds": ["subnet-291d8e44","subnet-f4c0a049"],
              "SecurityGroupIds": ["sg-2459beca217fdc703"],
              "VpcId": "vpc-41e9080b"}
```

## Raw values the emulator returned

RDS from `pulumi up` (`aws.rds.Instance("orders")`):

```
DBInstanceIdentifier orders3e4cbb1
Endpoint             172.17.0.3:7001
DBInstanceClass      db.t4g.micro       EngineVersion 17
DBInstanceStatus     available          AvailabilityZone us-east-1a
DbiResourceId        db-A508E36E461B4AD6B4CEBF59
DBInstanceArn        arn:aws:rds:us-east-1:000000000000:db:orders3e4cbb1
DBSubnetGroup        orders-73b9333 / vpc-41e9080b
                     subnet-291d8e44 (us-east-1a), subnet-f4c0a049 (us-east-1b)
VpcSecurityGroups    sg-2459beca217fdc703 (active)
```

RDS from `sst deploy` (`sst.aws.Postgres("Orders")`, which produced a plain
instance, not an Aurora serverless v2 cluster, on SST 3.19.3):

```
DBInstanceIdentifier with-sst-floci-ordersinstance-hdwswdvd
Endpoint             172.17.0.3:7002
DBInstanceClass      db.t4g.micro       EngineVersion 17
VpcId                vpc-f3e88d0a
```

`172.17.0.3` is floci's own address on the docker bridge; it runs one auth proxy
per instance on an allocated port and forwards to the postgres container's
`5432`, as its log says:

```
RDS proxy started for instance rds-resource:arn:aws:rds:us-east-1:000000000000:db:orders3e4cbb1
  on port 7001 -> 172.17.0.5:5432
RDS proxy TLS: generated CA cert at /app/data/tls/rds-ca.crt
DB instance orders3e4cbb1 created, engine=POSTGRES, endpoint=172.17.0.3:7001
```

The host reaches that address directly, which is how the `orders` table was
created (`psql -h 172.17.0.3 -p 7001 -U postgres -d orders`).

## The link records ocel stored

Published with `ocel link set --owner pulumi`, standing in for what
`@ocel/pulumi` would have shelled out to had red 2 not stopped it:

```json
{"name":"orders","postgres":{"host":"172.17.0.3","port":7001,"database":"orders","username":"postgres","password":"prototype"},"source":"pulumi"}
{"name":"network","custom":{"subnetIds":["subnet-291d8e44","subnet-f4c0a049"],"securityGroupIds":["sg-2459beca217fdc703"]},"source":"pulumi"}
```

`ocel link ls` read them back:

```
NAME     TYPE      SOURCE  OWNER   VERSION
network  custom    pulumi  pulumi  1
orders   postgres  pulumi  pulumi  1
```

`ocel link generate` wrote the transform types from the published records, which
is the surface `infra/network.transform.ts` reads:

```ts
interface Links {
  network: { securityGroupIds: string[]; subnetIds: string[] };
  orders: { host: string; port: number; database: string; username: string; password: string };
}
```

The whole link path — publish, list, read back, generate types — is green on
floci. Only the two IaC packages that are meant to drive it are not.

## Not attempted, and why

- **`ocel deploy` of examples/with-sst.** `sst deploy` never published a link
  (red 3), and `ocel deploy` was already known to die at the edge from the
  with-pulumi run (red 4); running it again would have produced the same
  CloudFront error against the same account-wide bootstrap.
- **`POST /orders` and `GET /orders` over HTTP.** No function URL, API Gateway
  or CloudFront distribution ever came into being — red 4 stops the deploy
  before the app is packaged. Reachability was answered by direct invocation
  instead, and the two probes ran the same two SQL statements the routes run.
- **Fixing either IaC package.** Out of remit for a planning prototype; both are
  filed above instead.
- **A second `ocel bootstrap` after red 1.** The first bootstrap's resources
  stood for the whole session, so nothing needed it.
- **Aurora serverless v2 on floci.** SST 3.19.3's `sst.aws.Postgres` produced a
  plain RDS instance with the example's settings, so `CreateDBCluster` was never
  exercised by this run.

## Teardown

Everything came down.

- `pulumi destroy --stack floci`: green, 1m31s, 12 deleted. The stack history
  remains in the scratch file backend, which is discarded with the scratch dir.
- `sst remove --stage floci`: green, 8s, every resource deleted.
- `ocel bootstrap destroy production --yes`: refused first —
  `the production bootstrap is still in use ... 1 project(s) are still deployed
  into it: with-pulumi`. `ocel destroy production --yes` in the example (green,
  <1s) cleared it, and the bootstrap destroy then went green in 1s, emptying all
  three buckets and deleting both CloudFormation stacks and the SSM parameters.
  Worth noting that the project counted as "deployed" even though every
  `ocel deploy` had failed at the edge.
- `docker rm -f` removed `floci-proto`, the three `floci-rdsprobe*` Lambda
  containers and the leftover `floci-rds-db-*` postgres container.

One container was deliberately left: `floci-ecr-registry`, created by this
session's SST bootstrap. Its name carries no emulator id, so it cannot be told
apart from a registry another floci on this machine may be using, and removing
it could break a concurrent session. Two floci containers from other sessions
(`ocel-847`, `ocel-847s`, `ocel-847c`) were left alone.
