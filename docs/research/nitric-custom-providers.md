# Nitric custom providers: the generic deployment wire and the Pulumi helper

## Short answer

- The wire is two streaming RPCs and nothing else: `Up(DeploymentUpRequest) returns (stream DeploymentUpEvent)` and `Down`. Everything Nitric can express about a deployment — spec, config, progress, result — fits in those two calls. Copy the shape: a small verb set whose every reply is a progress stream.
- A provider is a plain executable. The CLI parses `<org>/<name>@<semver>`, execs `$HOME/.nitric/providers/<org>/<name>-<version>` with `PORT` set to a free port, and dials gRPC. Nothing about Pulumi, Go, or Terraform is in that contract — a provider can be written in any protobuf language and avoid Pulumi entirely.
- `NewPulumiProviderServer(provider, runtime)` owns `Up`/`Down` so the vendor writes only per-concern functions. That inversion is the lesson to copy. What it buys: an inline Pulumi automation-API stack per nitric stack, engine events translated into typed `ResourceUpdate`s, refresh as an attribute, and `Down` implemented entirely by `stack.Destroy` — the provider's own code never runs on teardown.
- The anti-lesson is that the helper *is* Pulumi. `Up` calls `checkDependencies(checkPulumiAvailable, checkDockerAvailable)` and fails with `FailedPrecondition` before the provider's `Init` — a vendor using the helper cannot decline Pulumi, or Docker.
- Nitric's own non-Pulumi provider proves the cost. `NewTerraformProviderServer` implements the same service, but `Down` returns `Unimplemented` ("run terraform destroy against your stack state") and `Up` synthesizes CDKTF without sending a single stream event. Half the contract is unhonoured because the second helper was built as a parallel interface rather than under a shared one.
- `Order()` is a hook worth stealing only if resources are the unit. Nitric's default is a fixed type bucketing, not a dependency graph, and the doc-comment says changing it is not recommended.
- `Result()` — a last hook returning what the CLI prints — is worth copying; make it structured, not Nitric's single `string`.
- Attributes are an untyped `google.protobuf.Struct` that every provider `mapstructure`-decodes itself. Ocel's typed `ProviderConfig` oneof is the better end of that trade.
- Extensions are not a plugin system: they are Go struct embedding of `deploy.NitricAwsPulumiProvider` with selective method overrides. Only works if the base provider is an importable exported struct.
- The runtime half is a second binary, `go:embed`ed into the deploy binary as `[]byte`, injected into the user's container as `ENTRYPOINT ["/bin/runtime"]` wrapping their original `CMD`. One artifact, so versions cannot skew.

## 1. The generic wire

- The whole contract: `service Deployment { rpc Up (DeploymentUpRequest) returns (stream DeploymentUpEvent); rpc Down (DeploymentDownRequest) returns (stream DeploymentDownEvent); }`. https://github.com/nitrictech/nitric/blob/main/nitric/proto/deployments/v1/deployments.proto
- `DeploymentUpRequest` = `Spec spec`, `google.protobuf.Struct attributes` ("allows for adding project identifiers etc."), `bool interactive` ("a hint to the provider of the kind of output that the client can accept"). `DeploymentDownRequest` carries attributes and interactive only — **no spec**, so a provider must recover its own state to tear down. Same URL.
- `DeploymentUpEvent` is a oneof of `string message`, `ResourceUpdate`, `UpResult`. `ResourceUpdate` = resource id (nil means the stack itself), `ResourceDeploymentAction` (CREATE/UPDATE/REPLACE/SAME/DELETE), `ResourceDeploymentStatus` (PENDING/IN_PROGRESS/SUCCESS/FAILED), a `sub_resource` string "used when Nitric Resources map 1:many in a cloud provider", and a message. `UpResult` is `bool success` plus a single `string text`. Same URL.
- `Spec` is a flat `repeated Resource`; `Resource.config` is a oneof of 14 kinds — Service, Bucket, Topic, Api, Policy, Schedule, KeyValueStore, Secret, Websocket, Http, Queue, SqlDatabase, Batch, Website. `Service` carries an image URI, workers, env, and a free-form `type` string: "a provider can implement how this request is satisfied in any way". Same URL.
- Nothing on the wire expresses plan, diff, drift, or state location. Refresh is smuggled through `attributes["refresh"]`. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/provider/pulumi.go
- The docs confirm the contract is language-neutral: "our provider's are based on protocol buffer contracts which can be compiled to any language". https://github.com/nitrictech/docs/blob/main/docs/providers/custom/create.mdx

## 2. How the CLI finds and runs a provider

- `NewStandardProvider` matches `\w+\/\w+@<semver>`; `binaryFilePath()` is `<providerDir>/<org>/<name>-<version>` (`.exe` on Windows); `Start()` execs it with `PORT` set to a port the CLI just reserved and returns the address. Stop is `process.Kill()`. https://github.com/nitrictech/cli/blob/main/pkg/provider/standard.go
- Only `nitric`-org providers auto-install, from `github.com/nitrictech/nitric/releases/.../<name>_<os>_<arch>.tar.gz`; anything else "should be installed manually". Nitric-org versions below `1.0.0` are rejected except the dev `0.0.1`. Same URL, and https://github.com/nitrictech/cli/blob/main/pkg/provider/provider.go
- A second distribution form exists: `provider: docker://my-provider[:tag]`, gated behind the `docker-providers` preview flag. The CLI runs the image with the workspace and `/var/run/docker.sock` bind-mounted and a hardcoded port 50051. https://github.com/nitrictech/docs/blob/main/docs/providers/custom/docker.mdx, https://github.com/nitrictech/cli/blob/main/pkg/provider/image.go
- Stack files are `nitric.<stack>.yaml`; the file is `provider: <string>` plus everything else inline, which becomes the attributes struct. https://github.com/nitrictech/cli/blob/main/pkg/project/stack/stack.go
- The naming convention is documented as `[namespace]/[provider]@[version]`, and the CLI's job is separate from the provider's: build, run the app in resource-collection mode, then invoke the provider plugin. https://nitric.io/docs/get-started/foundations/deployment

## 3. What the Pulumi helper does for you

- `NewPulumiProviderServer(provider NitricPulumiProvider, runtime RuntimeProvider, options ...)` returns a server that registers itself as `Deployment` and serves on `$PORT`. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/provider/pulumi.go
- `Up` in order: dependency check (`pulumi` on PATH, Docker API ping) → read `project`/`stack` from attributes → `provider.Init(attributes)` → `auto.UpsertStackInlineSource("<project>-<stack>", project, program)` → `SetAllConfig(provider.Config())` → `optup.Refresh()` if `attributes["refresh"]` → `Up` with an event stream → read the stack output `nitric:stack:result` and send it as the terminal `UpResult`. Same URL.
- The inline program is the whole per-resource fan-out: `Order(spec.Resources)` → `Pre(ctx, resources)` → a type switch dispatching each resource to its method → `Post(ctx)` → `Result(ctx)` → `ctx.Export`. Panics inside are recovered into errors. Same URL.
- `Down` runs `Init` and `Config` and then `stack.Destroy` against an inline source with a **nil** program. No provider method is called; teardown correctness is entirely Pulumi state. Same URL.
- Pulumi engine events are translated into typed `ResourceUpdate`s — URNs mapped back to nitric resource ids, `apitype.Op*` mapped onto the action enum, unmapped ops (read, refresh, import) defaulting to UPDATE. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/pulumix/clistream.go
- The live interface has 18 methods; beyond the resource methods, `Init`, `Pre`, `Config`, `Order`, `Post`, `Result`. `Service` and `Batch` additionally receive the `RuntimeProvider`. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/provider/provider.go
- `NitricDefaultOrder` is a partial implementation providers embed. It sorts by a fixed type list — SqlDatabase, Batch, Service, Secret, Queue, Topic, Bucket, KeyValueStore, Api, Websocket, Website, Schedule, Http, Policy — because "topics may need to know about services in order to setup subscriptions". Same URL.
- Providers assert conformance with `var _ provider.NitricPulumiProvider = (*NitricAwsPulumiProvider)(nil)` and embed `provider.NitricDefaultOrder`; `Config()` is where the Pulumi provider versions get pinned. https://github.com/nitrictech/nitric/blob/main/cloud/aws/deploy/deploy.go
- The published interface has already drifted from the source: the docs show `Pre(ctx, []*deploymentspb.Resource)`, `Service(..., *deploymentspb.Service)`, an 11-entry default order, and no `Website`/`SqlDatabase`/`Batch`. https://github.com/nitrictech/docs/blob/main/docs/providers/custom/create.mdx

## 4. Avoiding Pulumi

- Nothing in the proto or the CLI requires Pulumi. Anything that serves `Deployment` on `$PORT` is a provider. https://github.com/nitrictech/cli/blob/main/pkg/provider/standard.go
- But the helper does. `Up` and `Down` both open with `checkDependencies(checkPulumiAvailable, checkDockerAvailable)`, returning `FailedPrecondition` before `Init`. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/provider/dependencies.go
- Nitric's answer is a second, parallel helper: `NitricTerraformProvider` + `NewTerraformProviderServer`. Its methods take `cdktf.TerraformStack` instead of `*pulumi.Context`, it drops `Result`, and it adds `CdkTfModules()` and `RequiredProviders()`. `Up` writes embedded modules to disk, builds a CDKTF app, configures one of ten backends from `attributes["backend"]`, runs the same Order/Pre/switch/Post shape, and calls `app.Synth()` — sending no stream events at all. `Down` returns `codes.Unimplemented`. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/provider/terraform.go
- Both AWS binaries are the same eight lines with a different constructor. https://github.com/nitrictech/nitric/blob/main/cloud/aws/cmd/deploy/main.go, https://github.com/nitrictech/nitric/blob/main/cloud/aws/cmd/deploytf/main.go
- The published consequence: Terraform providers are "IaC generating providers", need Node.js for CDKTF, are "currently in Preview", and "Generated Terraform should be reviewed before deployment to Production environments". https://github.com/nitrictech/docs/blob/main/docs/providers/terraform/index.mdx

## 5. Extensions

- "Extending a provider can replace any individual resource, or add `pre` or `post` configuration to your deployment." The mechanism is Go embedding: `type AwsExtensionProvider struct { deploy.NitricAwsPulumiProvider }`, override `Init`, `Config`, `Bucket`, `Policy`, `Service`, and delegate the rest with `a.NitricAwsPulumiProvider.Bucket(...)`. https://github.com/nitrictech/docs/blob/main/docs/providers/custom/extend.mdx
- The worked example swaps S3 for DigitalOcean Spaces and has to change both halves — the Pulumi resource and the runtime client's credentials and endpoint. Same URL.

## 6. The runtime half

- `type RuntimeProvider func() []byte`. https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/provider/runtime.go
- The AWS deploy binary `go:embed`s `runtime-aws`: "Embeds the runtime directly into the deploytime binary. This way the versions will always match as they're always built and versioned together (as a single artifact)". https://github.com/nitrictech/nitric/blob/main/cloud/aws/common/runtime/runtime.go
- The Makefile builds `cmd/runtime` for linux/amd64, copies it next to the embed directive, then builds `cmd/deploy`, and `install` drops the result at `$HOME/.nitric/providers/nitric/aws-0.0.1`. https://github.com/nitrictech/nitric/blob/main/cloud/aws/Makefile
- `Service()` hands `runtime()` to `image.NewImage`, which wraps the user's image with `COPY ${RUNTIME_FILE} /bin/runtime`, `CMD [<original command>]`, `ENTRYPOINT ["/bin/runtime"]` — the membrane becomes PID 1 and re-execs the app. https://github.com/nitrictech/nitric/blob/main/cloud/aws/deploy/service.go, https://github.com/nitrictech/nitric/blob/main/cloud/common/deploy/image/wrapper.dockerfile
- The membrane is one gRPC server with plugin fields — `GatewayPlugin`, `ResourcesPlugin`, `KeyValuePlugin`, `TopicsPlugin`, `StoragePlugin`, `SecretManagerPlugin`, `WebsocketPlugin`, `QueuesPlugin`, `SqlPlugin`, `BatchPlugin`, plus worker handlers — and it also manages the child process and blocks until `MinWorkers` register. https://github.com/nitrictech/nitric/blob/main/core/pkg/server/server.go
- A custom provider fills those slots in `cmd/runtime/main.go`; unimplemented services return `codes.Unimplemented`, so partial providers are legal. https://github.com/nitrictech/docs/blob/main/docs/providers/custom/create.mdx

## 7. Mapping onto Ocel's `Deploy`, and where it fails

- Ocel's `ProviderService` has 22 RPCs; `Deploy` is one of them and the lifecycle verbs — `Bootstrap`, `RemoveSubstrate`, `RemoveEnvironment`, `RemoveProject`, `Rollback`, `AddHostname`, `UsePreviewWildcard`, `RemoveStalePromotions` — are each their own streaming call. Nitric collapses all of that into `Up`/`Down`. `proto/provider/contract/v1/contract.proto`
- The fan-out analogy breaks on the unit. Nitric's helper slices a resource graph by resource kind, and the vendor writes `Bucket`, `Topic`, `Queue`. Ocel's unit is a whole release, so an Ocel helper has nothing to slice by kind — it would have to slice by lifecycle verb, and the vendor-supplied functions would be `Deploy`, `Rollback`, `AddHostname`, not `Bucket`.
- `Order()` therefore has no counterpart: with one release there is no per-resource sequence for a hook to permute; ordering is internal to `Deploy`.
- Nitric has no plan step at all; Ocel already has `PlanRemoveSubstrate`/`PlanRemoveProject`/`PlanRemovePreviewWildcard` returning a `RemovalPlan` before the destructive stream. That is the gap Nitric's `Down`-without-a-spec leaves open, already closed on Ocel's side.
- `Result()` maps cleanly onto Ocel's terminal `OperationEvent` — but Nitric's result is one `string`, printed to stdout, while Ocel's stream is already structured. Copy the hook, not the type.
- `google.protobuf.Struct attributes` maps onto Ocel's `ConfigureRequest`/`ProviderConfig`, which is a typed oneof (`AwsProviderConfig`). Nitric pushes decoding and validation into every provider; Ocel's typing is the better end of that trade and should stay. `proto/provider/contract/v1/contract.proto`
