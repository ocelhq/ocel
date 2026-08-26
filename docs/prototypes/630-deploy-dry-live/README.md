# Prototype: `deploy --dry` through the real CLI

Throwaway. Branch `proto/630-deploy-dry-live`, never merged. It exists so
[#630](https://github.com/ocelhq/ocel/issues/630) has a real run to argue with rather than
a scripted one.

[#624](https://github.com/ocelhq/ocel/issues/624) settled the shape against a
fidelity-reduced driver replaying invented streams. This branch removes the driver: the
renderer is the same code, and everything upstream of it is now the real CLI, the real
provider process, and the real Pulumi engine.

## What is actually wired

- **`ocel deploy --dry`** is a flag on the real command, carried on `DeployRequest.dry`.
- **The plan is an event on the progress stream.** `ChangePlan` moved out of the provider
  contract into `common/plan/v1` so `OperationEvent` can carry it — the contract imports
  progress, so the plan could not have ridden the stream while it lived in the contract.
  The unary `Plan*` RPCs still return the same messages.
- **The Pulumi adapter previews.** `Engine` gains `Preview`/`PreviewDestroy`;
  `ResourcePreEvent` metadata maps onto plan rows, which is
  [#615](https://github.com/ocelhq/ocel/issues/615)'s mapping written down.
- **The AWS releaser plans** by running the same stack program through preview, minus
  `publishBuild` — a plan must not upload.
- **The producer is app-major.** `deployStages` used to declare
  Uploading/Provisioning/Promoting as roots with apps nested underneath — phase-major, the
  shape #624 rejected. Units are the roots now (the environment, the infra stack, each app,
  promotion) and phases hang off them.
- **`runui.Run(ctx, deps, cfg, Spec{Command, Dry}, fn)`** is the command's entry
  ([#620](https://github.com/ocelhq/ocel/issues/620)), and `runui.Envelopes` turns the real
  `progressv1` stream into the envelope the #624 renderer already consumes.

## What is not, and why

**The live AWS half is not run yet.** A real preview needs a bootstrapped account, and
none of the accounts reachable from this machine has a current-CLI bootstrap. Bootstrapping
one provisions real, billed resources, so it is a decision rather than a step.

`captures/unbootstrapped.*` is how far the real path gets today: real CLI → real AWS
provider process → preflight refusal → rendered through the ported renderer. That already
exercised the whole chain end to end, which is how four of the defects below surfaced.

The plan-row mapping is covered without a cloud by `plan_live_test.go`
(`OCEL_LIVE_PULUMI=1`): a real Pulumi engine, a real preview, a `file://` backend, an
inline program of component resources. That is what caught the first two defects.

Two reductions are deliberate:

- `Planner` is an **optional** port, not the required `Releaser.Plan` that
  [#623](https://github.com/ocelhq/ocel/issues/623) specifies. Making it required drags vps,
  fake and conformance into a change that cannot answer this ticket's question.
- `providerkit.Plan` is still spelled `BootstrapPlan`. The rename is #623's, and it moves
  no pixels.

BuildKit remains absent from `main`, so the build interior is still whatever the CLI's
build writer emits, not vertices.

## Defects the real path found

1. **The root stack renders as a plan row.** A preview emits `pulumi:pulumi:Stack` and
   `pulumi:providers:*` steps. They are engine bookkeeping, not remote mutations, so
   [#618](https://github.com/ocelhq/ocel/issues/618)'s "a plan row is a remote mutation
   only" rules them out. Filtered here.
2. **Plan rows arrive in completion order, so they shuffle between runs.** The engine emits
   `ResourcePreEvent` as steps start, which is nondeterministic under `--parallel`. A plan
   you cannot diff against last week's plan is not a plan. Sorted by kind then name here.
3. **`deploy --dry` was not read-only.** Before it reached the preview it wrote a project
   record, reconciled the edge (a real mutation at the edge provider) and could auto-heal
   the bootstrap. Three writes on the path of a command whose whole promise is that it
   changes nothing. The dry path now branches before all three and admits through a
   non-healing `Gate.Inspect`.
4. **The failure block had no headline.** `✗` followed by nothing, because the result
   envelope only named a headline on success.
5. **The result was emitted twice** once the command stopped owning the closing line — the
   stream's own `ResultEvent` is the result, and a command that also calls `Finish` prints
   it again. Worth stating as a rule: with the stream as the product, the command does not
   get to conclude.
6. **Typed records flatten into presentation records.** `degraded` and the needs refusal
   used to reach `--log-format json` as their own typed shapes. Through the #624 renderer's
   NDJSON projection they arrive as a diagnostic and a result. That is the renderer emitting
   its own vocabulary rather than protojson of the stream, and it contradicts
   [#619](https://github.com/ocelhq/ocel/issues/619)'s "NDJSON = protojson per line,
   first-class". The projection needs to be generated from the envelope, not hand-written.
7. **`deployui.truncateToWidth` counts escape sequences as columns** — confirmed on `main`,
   as #624 reported. Still worth its own implementation issue.

## What this says about the #624 shape

Nothing in the app-major spine broke on contact. The producer change was the interesting
part: the display was only phase-major because `deployStages` was, and moving the units to
the roots was a smaller change than the renderer that had been compensating for it.

The one thing the real path adds that the scripted one could not: `Reporter.Detail` had no
stage, so every provider log line was already going to a global gutter. Under a scripted
stream that never showed, because the script assigned lines to blocks by hand.

## Still open

Unchanged from #624, and none of them are answerable without the live run:

- The row budget for the live window under a real fan-out.
- Whether a phase that only carried a progress bar should be a phase.
- Whether `promotion` and the edge deserve unit status.

And new here: whether the NDJSON projection should be generated from the proto rather than
hand-written per envelope kind (defect 6).

## Run it

```
make all
ocel deploy --dry --config ocel.proto630.config.ts --yes
OCEL_LIVE_PULUMI=1 go test ./pkg/providerkit/pulumi -run TestPreviewProducesPlanRows -v
```

The low-fidelity driver from #624 still builds and runs alongside it, for comparison:

```
go build -o /tmp/protoui ./cli/internal/cli/runui/protocmd
/tmp/protoui --variant=aws-container
```

## Known test fallout

`cli/internal/cli/deploy`'s suite fails on this branch. Every failure asserts the old
`deployui` vocabulary — the `Waiting`/`Ctrl-C` banner, the `provisioning...` progress
lines, the typed `degraded`/`failed` JSON records. Replacing the renderer is the point of
the branch; rewriting that suite belongs to the implementation issue, not the prototype.
Every other module's tests pass.
