# Can the with-sst and with-pulumi journeys run on floci?

Research for [#834](https://github.com/ocelhq/ocel/issues/834), part of the test-suite map
[#830](https://github.com/ocelhq/ocel/issues/830). Findings only; nothing here is a decision.

## Verdict

**Yes for Pulumi, probably yes for SST, and neither is proven end to end.** Every mechanism the
two journeys need exists: both tools can be pointed at an arbitrary AWS endpoint, and floci
serves the whole VPC + security-group + RDS path those examples provision — including a real
PostgreSQL container behind `CreateDBInstance` and `CreateDBCluster`. What is missing is a run:
nobody has executed `pulumi up` or `sst deploy` against floci. The residual risk is not "can the
endpoint be overridden" (settled) but "does a bridged Terraform provider making hundreds of calls
find a gap floci has not filled" (unknown, and only a run answers it).

So these cells are **candidates for PR journeys, gated on a prototype**, not dispatch-only.

The one hard finding against: floci does not serve the AppSync **Events** API. That costs `sst dev`
(Live), not `sst deploy`, so it does not block the journey as scoped.

## First, floci is not LocalStack

The ticket assumes a LocalStack-based emulator. It is not one. `scripts/floci.sh` runs
`floci/floci:latest`, whose image labels name `https://github.com/floci-io/floci`, MIT, version
2.0.1, described as an "open-source alternative to LocalStack Community". It listens on 4566 and
answers `/_localstack/health` for wire compatibility, but it is an independent Java
implementation. LocalStack's own Community edition sunset in March 2026
([blog.localstack.cloud](https://blog.localstack.cloud/the-road-ahead-for-localstack/)), which is
presumably why the repo moved.

This matters for the ticket's framing: LocalStack's community/pro split — which is what put RDS
and most of VPC behind a paid tier — does not apply. floci has a single tier and ships every
service ([floci.io/aws](https://floci.io/aws/): "No Auth Token · Open Source Forever").

## Can SST v3 target a custom endpoint?

**Provisioning: yes, by a confirmed passthrough.** SST's docs describe `providers.aws` as a
Pulumi passthrough and link to the Pulumi AWS provider's inputs rather than enumerating keys
([sst.dev/docs/providers](https://sst.dev/docs/providers/)). The code confirms it is a literal
passthrough — `pkg/project/run.go` forwards every provider key to the Pulumi CLI as a config flag,
filtering exactly one SST-only key:

```go
for provider, opts := range p.app.Providers {
    for key, value := range opts.(map[string]interface{}) {
        if key == "package" {
            continue
        }
        switch v := value.(type) {
        case map[string]interface{}:
            bytes, err := json.Marshal(v)
            ...
            args = append(args, "--config", fmt.Sprintf("%v:%v=%v", provider, key, string(bytes)))
```

([sst/sst, `pkg/project/run.go`](https://github.com/sst/sst/blob/dev/pkg/project/run.go)) So
`providers: { aws: { endpoints: [...], skipCredentialsValidation: true, s3UsePathStyle: true } }`
reaches the Pulumi AWS provider intact.

**Bootstrap: not a passthrough, but it does not need to be.** SST's Go core resolves credentials
through plain `aws-sdk-go-v2` `config.LoadDefaultConfig` and builds S3/ECR/SSM/STS clients with
`NewFromConfig` and no `BaseEndpoint` override
([`pkg/project/provider/aws.go`](https://github.com/sst/sst/blob/dev/pkg/project/provider/aws.go)).
That is the same SDK, and the same default-chain resolution, that `AWS_ENDPOINT_URL` redirects —
which is precisely how this repo already drives floci today
(`platform/aws/provider/livefloci_test.go:36` sets `AWS_ENDPOINT_URL` and nothing else). So the
env var covers bootstrap while the provider config covers provisioning.

Bootstrap creates an asset bucket, a versioned state bucket with an SSL-deny bucket policy, an
`sst-asset` ECR repo, and an SSM parameter. The AppSync step is dead code — the source comments it
"add appsync events apis for live lambda - we no longer do this" and returns nil; the Events API is
created lazily by `ResolveAppSync()` for Live. All four bootstrap resources answered on floci (see
the probe log below).

**Maintainer position.** Dax closed [sst/sst#4359](https://github.com/sst/sst/issues/4359) on
2024-08-01 with "ultimately getting that to work with localstack has a lot of challenges so this
isn't something we're likely going to support." That is a statement about support, not about
mechanism, and it predates floci by eighteen months. It is a reason to expect no help when
something breaks, not a reason to believe it cannot work.

## Can Pulumi target a custom endpoint?

Yes, and it is the documented path. The AWS provider takes `endpoints`,
`skipCredentialsValidation` ("Skip the credentials validation via STS API. Used for AWS API
implementations that do not have STS available/implemented"), `skipRequestingAccountId`,
`skipMetadataApiCheck` and `s3UsePathStyle`
([registry docs](https://www.pulumi.com/registry/packages/aws/installation-configuration/)).
LocalStack ships a `pulumilocal` wrapper that rewrites ~200 endpoints to `localhost:4566`
([docs.localstack.cloud](https://docs.localstack.cloud/aws/connecting/infrastructure-as-code/pulumi/));
the same stack-config shape works against floci, which serves the same port. State does not need
to live in the emulator — `PULUMI_BACKEND_URL=file://…` is the documented local backend and is
recommended, not required.

The one documented exclusion is `aws-native` (it rides Cloud Control). Neither example uses it.

## Does the RDS + VPC + security-group path exist on floci?

Probed directly against `floci/floci:2.0.1` on 2026-09-03, via `scripts/floci.sh create`. Every
call below returned a well-formed success response.

| Call | Result |
| --- | --- |
| `sts:GetCallerIdentity` | account `000000000000` |
| `ec2:DescribeAvailabilityZones` | `us-east-1a/b/c` |
| `ec2:CreateVpc`, `CreateSubnet`, `CreateRouteTable`, `AssociateRouteTable` | ids returned, `State: available` |
| `ec2:CreateInternetGateway`, `AttachInternetGateway` | ok |
| `ec2:AllocateAddress`, `CreateNatGateway` | `State: available` immediately |
| `ec2:CreateSecurityGroup`, `AuthorizeSecurityGroupIngress` (self-referential, 5432) | rule id returned with `ReferencedGroupInfo` |
| `ec2:CreateVpcEndpoint` Gateway (s3) | `State: available`, route table recorded |
| `ec2:CreateVpcEndpoint` Interface (kms, private DNS) | `State: available`, subnet + group recorded |
| `rds:CreateDBSubnetGroup` | `SubnetGroupStatus: Complete` |
| `rds:CreateDBInstance` (postgres 17, db.t4g.micro, in the VPC/SG) | `available` |
| `rds:CreateDBCluster` (aurora-postgresql, serverless v2) + `db.serverless` instance | `available` |
| `s3:CreateBucket` + `PutBucketVersioning` + `PutBucketPolicy` | versioning `Enabled`, policy accepted |
| `ecr:CreateRepository` | uri `000000000000.dkr.ecr.us-east-1.localhost:5100/sst-asset` |
| `ssm:PutParameter` | version 1 |
| `appsync:CreateApi` (Events) | **no response — unsupported**; `ListGraphqlApis` works |

`CreateDBInstance` is not a mock. `docker ps` afterwards showed
`floci-rds-db-2161941E217D4BADA59814D3-c28f1b  postgres:17-alpine`, and the instance's endpoint
came back as `172.17.0.3:7001` — a Docker bridge address on a per-instance allocated port, matching
floci's documented design ("Floci manages real PostgreSQL, MySQL, and MariaDB Docker containers and
proxies TCP connections to them", [floci.io/floci/services/rds](https://floci.io/floci/services/rds/)).
The Aurora cluster got `172.17.0.3:7002`, its writer `:7003`.

Three shapes to know about, none fatal:

- **The port is not 5432.** Both examples publish `orders.port` rather than a literal, so they are
  already correct; anything that assumes 5432 is not.
- **The host is a bare IP, not a DNS name.** `scripts/e2e-sst/assert-published.mjs` and friends
  should be read for hostname-shaped assertions before they are ported.
- **Security groups are not a firewall.** floci says so outright: "Security group rules are not
  enforced as a firewall (Docker bridge networking handles routing)"
  ([floci.io/floci/services/ec2](https://floci.io/floci/services/ec2/)). The same goes for VPC
  isolation. A journey can therefore assert that ocel *placed* the Lambda in the right subnets and
  security groups — which is what `infra/network.transform.ts` exists to prove, and what the
  current README's `get-function-configuration --query VpcConfig` check already does — but it can
  never prove the network *isolated* anything. That assertion only lives on real AWS.

Also observed: the cluster echoed `VpcSecurityGroups: [sg-00000000]` rather than the group id
passed to `CreateDBCluster`, while `CreateDBInstance` echoed the real one. Worth a floci issue if a
journey ever asserts on it.

## A trap that cost an hour

Every `rds` call failed with `InvalidClientTokenId` while `s3`, `ec2`, `iam`, `sts` and
`secretsmanager` succeeded against the same endpoint with the same credentials. This was not floci:
a stray `~/.aws/config` on the machine took precedence for that service, and `AWS_ENDPOINT_URL` was
silently not applied. `aws --endpoint-url ...` worked immediately. Any harness that shells out to
the AWS CLI or an SDK should pin `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` at empty or a
fixture, not merely set `AWS_ENDPOINT_URL`.

## What would have to change

**`scripts/e2e-sst`** — the map already deletes it, folding its publish/consume/refuse assertions
into journeys. What survives the move needs:

- `guard-accounts.sh` (`scripts/e2e-sst/guard-accounts.sh:13`) refuses unless
  `sts:GetCallerIdentity` matches `EXPECTED_AWS_ACCOUNT_ID`. On floci that is always
  `000000000000`. The guard is a real-money safety rail and should stay for dispatch runs; it must
  be skipped, not satisfied, on the emulator — a run against floci has nothing to guard.
- `aws.mjs` shells out to the `aws` CLI throughout. It inherits ambient config; see the trap above.
- `lib.mjs` renders `sst.config.ts` from a template (`renderSstConfig`). Endpoint config is a new
  branch in that template, not a new file. But the map says configs are committed, never generated,
  so this template disappears with the harness — the endpoint override then has to live in the
  committed `examples/with-sst/sst.config.ts` behind an env read.
- `publish.mjs` runs `npx sst deploy --stage <stage>` (`scripts/e2e-sst/publish.mjs:14`), inheriting
  the environment. `AWS_ENDPOINT_URL` reaches bootstrap for free.
- `lib.mjs` renders an `rds-db:connect` grant against `orders.nodes.cluster.clusterResourceId`.
  floci returns a `DbClusterResourceId` and claims IAM database authentication, but the grant is
  never *enforced*, so this assertion degrades to a shape check on the emulator.

**`examples/with-sst/sst.config.ts`** — `providers: { aws: { region } }` gains endpoint keys when an
emulator endpoint is in the environment. The rest stands: `sst.aws.Vpc` and `sst.aws.Postgres`
provision the NAT gateways, subnets, security groups and Aurora cluster that floci served above.

**`examples/with-pulumi`** — `index.ts` reads `aws.config.requireRegion()` and builds resources on
the implicit default provider. Endpoints go in `Pulumi.<stack>.yaml` as `aws:endpoints` etc., or an
explicit `new aws.Provider(...)` threaded through every resource. Stack config is the smaller
change and keeps `index.ts` untouched. `Pulumi.yaml` also needs a stack and a backend — the journey
should set `PULUMI_BACKEND_URL=file://` and `PULUMI_CONFIG_PASSPHRASE` so no Pulumi Cloud account
is involved. `dbPassword` is a required secret today; a journey needs a value for it.

**Both examples** — the `com.amazonaws.<region>.{s3,dynamodb,kms}` VPC endpoints exist to let a
Lambda in a private subnet reach AWS services without a NAT. On floci they are inert CRUD. They can
stay (they cost nothing and keep the example honest about real AWS) but they prove nothing on the
emulator.

**Runtime** — the app must actually connect to the published Postgres. floci runs Lambda as Docker
containers and the RDS container is on the same bridge, so `172.17.0.3:7001` is plausibly
reachable; floci documents neither a guarantee nor a counterexample. This is the single highest-risk
unknown and the first thing a prototype should check.

## What is not proven

1. `pulumi up` has never been run against floci in this repo, and floci's README lists Terraform
   (67 compat tests), OpenTofu (41) and CDK (20) as tested integrations — **Pulumi is not in that
   matrix**, though [floci#1043](https://github.com/floci-io/floci/issues/1043) shows users running
   it and hitting at least one gap. Pulumi's AWS provider is bridged from the Terraform one, so the
   Terraform coverage is meaningful evidence, not proof.
2. `sst deploy` has never been run against floci by anyone, as far as any primary source shows.
3. Whether a floci Lambda can open a TCP connection to a floci RDS container.
4. How long `sst deploy` takes on an emulator — the journey workflow has a budget, and SST's
   bootstrap plus a VPC plus an Aurora cluster is not a small plan.

The cheap way to close 1–3 is a timeboxed prototype: one throwaway Pulumi stack with a VPC, a
security group and one `aws.rds.Instance` against a floci container, then the same for SST. If both
apply clean, the cells are journeys. If either wedges, that cell is dispatch-only and the other can
still be a journey — the two examples are independent.
