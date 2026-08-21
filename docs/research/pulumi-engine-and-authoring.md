# Pulumi as an embedded engine and as a provider-authoring model

## Short answer

- Two independent adoptions. `pulumi-go-provider` is a way to *write* a provider; the Automation API is a way to *run* Pulumi programs from Go. Neither implies the other, and Ocel already uses the second without the first.
- The Automation API is not a library that provisions. It is a typed wrapper over the `pulumi` CLI: "this still requires a CLI binary to be installed and available on your $PATH." A `providerkit/pulumi` adapter must own the binary — locate, pin, download — before it owns anything else, exactly as `pulumiruntime.Ensure` does today.
- There is no backend-less mode. A Pulumi program needs only `PULUMI_MONITOR`/`PULUMI_ENGINE` gRPC addresses, but nothing in `sdk/` serves them; the engine, state and backends live in `pulumi/pkg`. A "pulumi-less" provisioning port would own the resource monitor, the checkpoint format, diffing and deletion order — not just a state file.
- The adapter's substrate contract is small and fixed: backend URL, passphrase, project name, plus the CLI handle. That is precisely `deploy.PulumiAccess` today. Everything else — region, tags, buckets — is vendor payload and belongs in the program func, not the adapter.
- Backend and secrets are workspace-level, not stack-level. `auto.SecretsProvider` plus `PULUMI_BACKEND_URL`/`PULUMI_CONFIG_PASSPHRASE` in `auto.EnvVars` is the whole configuration surface for a DIY S3 backend; the adapter should refuse to construct a workspace when any of the three is empty rather than let the CLI prompt.
- Stack naming is the adapter's constraint to enforce, not the vendor's: alphanumerics, hyphens, underscores and periods only, unique within a project, and on a DIY backend the org segment is always the literal `organization`.
- Concurrency is per stack, and detectable. The engine takes a file lock under `.pulumi/locks/`; `auto.IsConcurrentUpdateError` matches "the stack is currently locked by". Different stacks in one project run concurrently; one stack does not. Parallelism *within* a stack is `optup.Parallel`.
- The vendor writes two things and nothing else: a `pulumi.RunFunc` and a decoder from `auto.OutputMap` to typed results. Progress, tracing, tagging, locking, teardown and stack enumeration are adapter concerns — `runStack` in `deploy/stackrun.go` is already that shape and should move wholesale.
- Keep `github.com/pulumi/pulumi/sdk/v3` in its own Go module. `platform/aws/provider` is already a separate module in `go.work`; a non-Pulumi vendor that never imports the adapter never sees the dependency, and no `replace` is needed.
- `infer` is worth adopting only if Ocel wants its resources consumable from Pulumi programs. It buys schema generation and cross-language SDKs; it costs a `pulumi-resource-*` binary, a registry story, and the whole plugin-acquisition path — for capabilities Ocel's own CLI does not consume.

## 1. What `pulumi-go-provider` owns

- The framework runs the gRPC server, generates the schema, serialises state, handles secrets, and supplies structural diffing and default implementations when the author supplies none. https://pkg.go.dev/github.com/pulumi/pulumi-go-provider
- Schema inference is by Go reflection over the author's types, which is what removes hand-authored `schema.json`. https://www.pulumi.com/docs/iac/guides/building-extending/packages/pulumi-go-provider-sdk/
- The engine calls seven lifecycle RPCs on any provider: `Check`, `Diff`, `Create`, `Read`, `Update`, `Delete`, `Configure`. A provider is a gRPC server the engine launches and talks to. https://www.pulumi.com/docs/iac/using-pulumi/extending-pulumi/build-a-provider/
- Provider binaries are found by name: `pulumi-resource-<name>`. https://www.pulumi.com/docs/iac/using-pulumi/extending-pulumi/build-a-provider/
- SDKs for every language come from `pulumi package gen-sdk <provider>`; `pulumi package get-schema` dumps the inferred schema. https://github.com/pulumi/pulumi-go-provider/blob/main/README.md

## 2. What the author writes

- The minimum is `CustomCreate[I, O]` — `Create(ctx, CreateRequest[I]) (CreateResponse[O], error)`. Everything else is optional. https://pkg.go.dev/github.com/pulumi/pulumi-go-provider/infer
- Optional capability interfaces, each opted into by implementing a method: `CustomCheck[I]`, `CustomDiff[I,O]`, `CustomUpdate[I,O]`, `CustomRead[I,O]`, `CustomDelete[O]`, `CustomStateMigrations[O]`, `CustomConfigure`, `Annotated`. https://pkg.go.dev/github.com/pulumi/pulumi-go-provider/infer
- The defaults are consequential: without `Diff` the diff is structural; without `Update` any field change forces a replace; without `Delete` deletion is a no-op. https://github.com/pulumi/pulumi-go-provider/blob/main/README.md
- `Annotator` carries docs and defaults into the schema — `Describe`, `SetDefault(i, v, env...)`, `SetToken`, `AddAlias`, `Deprecate`. https://pkg.go.dev/github.com/pulumi/pulumi-go-provider/infer
- Provider configuration is a typed struct read back with `infer.GetConfig[T](ctx)`. https://pkg.go.dev/github.com/pulumi/pulumi-go-provider/infer
- `main()` is a builder plus a run call: `infer.NewProviderBuilder().WithResources(infer.Resource(&R{})).Build()` then `provider.Run(ctx, "name", "version")`. The lower-level path is the `Provider` struct with `RunProvider`/`RunProviderF`. https://www.pulumi.com/docs/iac/guides/building-extending/packages/pulumi-go-provider-sdk/, https://pkg.go.dev/github.com/pulumi/pulumi-go-provider

## 3. Components

- `ComponentResource[I, O]` needs one method, `Construct(ctx *pulumi.Context, name, typ string, args I, opts pulumi.ResourceOption) (O, error)`, and the framework registers outputs. https://pkg.go.dev/github.com/pulumi/pulumi-go-provider/infer
- Components have no CRUD lifecycle and no state of their own; they group resources that real providers provision. Type names must read `<package>:index:<Name>`. https://www.pulumi.com/docs/iac/guides/building-extending/components/build-a-component/
- Consumers pull them in with `pulumi package add` or a `packages` block in `Pulumi.yaml`; a component-only package needs no provider SDK at all. https://www.pulumi.com/docs/iac/guides/building-extending/components/build-a-component/

## 4. Embedding: the CLI is the runtime

- "The Automation API is the programmatic interface for driving Pulumi programs without the CLI… this still requires a CLI binary to be installed and available on your $PATH." https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto
- Either pre-install and rely on `PATH`, or install programmatically from the program itself. https://www.pulumi.com/docs/iac/using-pulumi/automation-api/concepts-terminology/
- `InstallPulumiCommand(ctx, opts)` downloads a pinned version; `NewPulumiCommand(opts)` binds to an existing one; `PulumiCommandOptions` carries `Version`, `Root`, `SkipVersionCheck`. https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto
- Three program sources: local (filepath), remote (git), inline (a Go func). Inline is the only one with no `Pulumi.yaml` on disk, and "the program's lifecycle must be fully contained within the function". https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto, https://www.pulumi.com/docs/iac/using-pulumi/automation-api/
- Workspace options: `Pulumi(cmd)`, `SecretsProvider(s)`, `EnvVars(m)`, `Project(settings)`, `WorkDir(p)`, `PulumiHome(d)`, `Program(fn)`. Stack constructors: `NewStackInlineSource`, `SelectStackInlineSource`, `UpsertStackInlineSource`. https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto

## 5. Backends, secrets, state

- Backends: Pulumi Cloud, or DIY over S3, Azure Blob, GCS, an S3-compatible server, PostgreSQL, or the local filesystem. https://www.pulumi.com/docs/iac/concepts/state-and-backends/
- URL forms: `file://<path>` (`--local` = `file://~`), `s3://<bucket>` with `region`/`profile`/`endpoint`/`disableSSL`/`s3ForcePathStyle`/`awssdk=v2`, `azblob://<container>` with `storage_account`, `gs://<bucket>`, `postgres://…`, `https://` for self-hosted Cloud. Credentials come from the ambient cloud SDK chain. https://www.pulumi.com/docs/iac/operations/stack-management/using-a-diy-backend/, https://www.pulumi.com/docs/iac/cli/commands/pulumi_login/
- `PULUMI_BACKEND_URL` selects the backend without a login step. https://www.pulumi.com/docs/iac/cli/environment-variables/
- DIY backends "cannot transparently recover from certain kinds of partial failures", being built on non-transactional blob storage; history lives in `.pulumi/history/`. https://www.pulumi.com/docs/iac/concepts/state-and-backends/
- Secrets providers are `default`, `passphrase`, `awskms`, `azurekeyvault`, `gcpkms`, `hashivault`, selected as a URL such as `awskms://key-id?region=us-east-1`. https://www.pulumi.com/docs/iac/concepts/secrets/
- `PULUMI_CONFIG_PASSPHRASE` (or `PULUMI_CONFIG_PASSPHRASE_FILE`) supplies the passphrase; a resource's `id` is always plaintext in state and cannot be encrypted. https://www.pulumi.com/docs/iac/cli/environment-variables/, https://www.pulumi.com/docs/iac/concepts/secrets/

## 6. Plugins

- Plugins cache under `~/.pulumi/plugins` (CLI-bundled ones under `~/.pulumi/bin`); `PULUMI_HOME` moves the root. Names are `pulumi-<kind>-<name>`. https://www.pulumi.com/docs/iac/concepts/plugins/
- "When you first run `pulumi preview` or `pulumi up`, the Pulumi CLI will install any required providers that are not already in your plugin cache." https://www.pulumi.com/docs/iac/concepts/plugins/
- Explicit acquisition exists for prewarming or air-gapped runs: `Workspace.InstallPlugin(ctx, name, version)` and `InstallPluginFromServer`; `PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION` turns implicit download off. https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto, https://www.pulumi.com/docs/iac/cli/environment-variables/

## 7. Stacks, concurrency, lifecycle

- Stack names allow alphanumerics, hyphens, underscores and periods, must be unique within a project, and accept `stack`, `org/stack` or `org/project/stack`. On a DIY backend the org segment "must always be the constant value `organization`". https://www.pulumi.com/docs/iac/concepts/stacks/
- Stacks in DIY backends are project-scoped since v3.61.0; older layouts need the irreversible `pulumi state upgrade`. https://www.pulumi.com/docs/iac/operations/stack-management/using-a-diy-backend/
- "A basic file-based locking system is enabled by default for all DIY backends," with lock files in `.pulumi/locks/`. https://www.pulumi.com/docs/iac/operations/stack-management/using-a-diy-backend/
- `auto.IsConcurrentUpdateError` matches `[409] Conflict: Another update is currently in progress.` and `the stack is currently locked by`; siblings cover select-404, create-409, compilation and runtime errors. https://github.com/pulumi/pulumi/blob/master/sdk/go/auto/errors.go
- Stack methods: `Up`, `Preview`, `Refresh`, `Destroy`, `Outputs`, `Export`/`ImportStack`, `Cancel`. Workspace methods: `ListStacks`, `RemoveStack`, `StackSettings`, `SaveStackSettings` — the orphan-stack path. https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto
- `optup` options that matter to an adapter: `Parallel`, `EventStreams`, `ProgressStreams`, `ErrorProgressStreams`, `Refresh`, `Target`/`TargetDependents`, `ContinueOnError`, `SuppressProgress`, `Message`, `Color`. https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/auto/optup
- Engine events are a sequence-numbered JSON stream of `PreludeEvent`, `ResourcePreEvent`, `ResOutputsEvent`, `ResOpFailedEvent`, `DiagnosticEvent`, `SummaryEvent`, `CancelEvent`, `StdoutEngineEvent`, `PolicyEvent`. https://pkg.go.dev/github.com/pulumi/pulumi/sdk/v3/go/common/apitype
- `PULUMI_PARALLEL`, `PULUMI_SKIP_UPDATE_CHECK`, `PULUMI_SKIP_CHECKPOINTS` (experimental; keeps only the final state) and `PULUMI_DIY_BACKEND_PARALLEL` are the throughput knobs. https://www.pulumi.com/docs/iac/cli/environment-variables/

## 8. Running without a backend

- A Pulumi program needs only `PULUMI_MONITOR`, `PULUMI_ENGINE`, `PULUMI_PROJECT`, `PULUMI_STACK`, `PULUMI_DRY_RUN`, `PULUMI_PARALLEL`, `PULUMI_CONFIG`; `RunErr` "executes the body of a Pulumi program, granting it access to a deployment context". https://github.com/pulumi/pulumi/blob/master/sdk/go/pulumi/run.go
- Nothing in `sdk/` implements that monitor. A backend "is an API and storage endpoint used by the CLI to coordinate updates", and the CLI prompts for one before anything touching stacks or state. https://www.pulumi.com/docs/iac/concepts/state-and-backends/
- So a backend-free port owns the monitor server, the checkpoint, diff, dependency ordering and deletion — a reimplementation of the engine, not a substitution for the state file.

## 9. What Ocel does today

- `pulumiruntime.Ensure` pins `3.146.0`, roots the install at `~/.ocel/pulumi/<version>`, probes with `auto.NewPulumiCommand` to decide whether to announce a download, then calls `auto.InstallPulumiCommand`. platform/aws/provider/pulumiruntime/runtime.go:13-40
- `deploy.PulumiAccess` is exactly `{Region, BackendURL, Passphrase, PulumiProject, Command}`, and `workspace()` turns it into `auto.Pulumi`, `auto.SecretsProvider("passphrase")`, `auto.EnvVars`. platform/aws/provider/deploy/deps.go:10-24
- The env map is `PULUMI_BACKEND_URL`, `PULUMI_CONFIG_PASSPHRASE`, `AWS_REGION`, `PULUMI_SKIP_CHECKPOINTS`, `PULUMI_SKIP_UPDATE_CHECK`. platform/aws/provider/deploy/deploy.go:21-29
- `prepareStack` is `auto.UpsertStackInlineSource` plus tag stamping through `SaveStackSettings` on `aws:defaultTags`, stack-index realisation and expiry stamping; `runStack` is `optup.Parallel(64)` plus `ProgressStreams` plus an `EventStreams` drain. platform/aws/provider/deploy/stackrun.go:281-329
- Tracing consumes the same event channel into spans keyed on `apitype.OpType` and URN-derived names, with a 30 s drain grace. platform/aws/provider/deploy/pulumitrace.go:1-60, platform/aws/provider/deploy/stackrun.go:252-271
- Vendor-specific work is confined to the `pulumi.RunFunc` closures and the `auto.OutputMap` decoders `collectLinks` and `collectAppFunctionOutputs`. platform/aws/provider/deploy/stackrun.go:27-58, :356-412
- `platform/aws/provider` is its own module in `go.work`, so the Pulumi SDK is already absent from `cli`, `sdk`, `pkg/*` and the Cloudflare edge module. go.work:3-12
