# Prototype: `deploy --dry` through the real CLI

Throwaway. Branch `proto/630-deploy-dry-live`, never merged. It exists so
[#630](https://github.com/ocelhq/ocel/issues/630) has a real run to argue with rather than
a scripted one.

[#624](https://github.com/ocelhq/ocel/issues/624) settled the shape against a
fidelity-reduced driver replaying invented streams. This branch removes the driver: the
renderer is the same code, and everything upstream of it is the real CLI, the real provider
process, and the real Pulumi engine against a real AWS account.

## The run

`captures/aws-preview.dry.*` is one command, three projections. Real preview, real type
tokens, real durations:

```
✓ api › building  1s

✓ proto630--infra › provisioning  10s

✓ api › provisioning  11s
proto630, fronted by the cloudflare edge:

  stack api
    + role-app                               aws:iam/role:Role
    + role-app-policy-vars-read              aws:iam/rolePolicy:RolePolicy
    + role-app-policy-logs                   aws:iam/rolePolicyAttachment:RolePolicyAttachment
    + proto630-proto630-api-index-rab08e86a  aws:lambda/function:Function
    + fn-index-url                           aws:lambda/functionUrl:FunctionUrl
    + layer-membrane                         aws:lambda/layerVersion:LayerVersion

  edge cloudflare/edge
    ~ cloudflare/edge  edge  — the edge reconciles and promotes once every app is provisioned

6 to create, 1 to update. 1 unchanged.

✓ Planned in 34s
```

**#624's shape holds.** Nothing in the app-major spine broke on contact with a real engine.
Plain and live commit the same lines in the same order — checked by stripping the live
window from the TTY capture and matching it against the plain one, normalising only
durations and the artifact content hash.

## What is wired

- **`--dry`** on `ocel deploy` and `ocel preview up`, carried on `DeployRequest.dry`.
- **The plan is an event on the progress stream.** `ChangePlan` moved out of the provider
  contract into `common/plan/v1` so `OperationEvent` can carry it — the contract imports
  progress, so a plan could not have ridden the stream while it lived in the contract.
- **The Pulumi adapter previews**, mapping engine step metadata onto plan rows.
- **The AWS releaser plans** by running the same stack program through preview, minus
  `publishBuild` — a plan must not upload.
- **The producer is app-major.** `deployStages` declared Uploading/Provisioning/Promoting as
  roots with apps nested underneath — phase-major, the shape #624 rejected. Units are the
  roots now and phases hang off them.
- **`runui.Run(ctx, deps, cfg, Spec{Command, Dry}, fn)`** is the entry
  ([#620](https://github.com/ocelhq/ocel/issues/620)); `runui.Envelopes` turns the real
  `progressv1` stream into the envelope the #624 renderer already consumes.

## What is not

- **The apply half is not run.** `ocel preview up` without `--dry` needs a real preview
  wildcard in Cloudflare; this run used `*.proto630.example.com`, which nothing serves. So
  "a real deploy applies and streams progress through the same path" is still owed.
- **BuildKit is still absent from `main`**, so the build interior is whatever the CLI's
  build writer emits, not vertices. Here it emits nothing at all, which is why the
  `api › building` block is a header alone.
- Two deliberate reductions: `Planner` is an **optional** port rather than the required
  `Releaser.Plan` [#623](https://github.com/ocelhq/ocel/issues/623) specifies, and
  `providerkit.Plan` is still spelled `BootstrapPlan`. Both are #623's work and neither
  moves a pixel.

## Defects the real path found

The first two came from a real engine with no cloud (`plan_live_test.go`); the rest needed
the whole chain.

1. **The root stack renders as a plan row.** A preview emits `pulumi:pulumi:Stack` and
   `pulumi:providers:*` steps — engine bookkeeping, not remote mutations, which
   [#618](https://github.com/ocelhq/ocel/issues/618) rules out. Filtered.
2. **Plan rows arrive in completion order, so they shuffle between runs.** Steps are
   emitted as they start, which under `--parallel` is nondeterministic. A plan you cannot
   diff against last week's plan is not doing a plan's job. Sorted by kind then name.
3. **KEEP rows never reached the plan.** The engine announces unchanged resources through
   `ResOutputsEvent`, not `ResourcePreEvent`, so reading only pre-events silently drops
   every KEEP — and #618 wants them in the tally. Both events feed the collector now.
4. **`deploy --dry` was not read-only.** Before reaching the preview it recorded the
   project, reconciled the edge (a real mutation, at the edge provider) and could auto-heal
   the bootstrap. Three writes on the path of a command whose whole promise is that it
   changes nothing. The dry path branches ahead of all three and admits through a
   non-healing `Gate.Inspect`.
5. **The dry path crashed the provider process** — `unexpected EOF` at the CLI. It reused
   the apply's result builder, which dereferences the edge stack that `--dry` deliberately
   never reconciles. A plan needs its own result, not the apply's minus some fields.
6. **An app cannot be planned without uploading it.** The app stack program resolves each
   function's S3 key from `r.artifacts`, which only `uploadApp` fills, so the plan died
   with `place fn--api--index's code: this provider keeps no "" store`. The key is a digest
   of local files, so it is computable without uploading, and dry mode now derives it and
   stops. This is the sharpest confirmation of #618's artifacts-are-engine-resources
   decision: until artifact uploads are engine resources, plan and apply cannot share a
   path without a special case exactly here.
7. **One app rendered as two units.** The CLI minted a stage id for the build and the kit
   minted another for the same app, so `api` appeared twice at the root. Unit identity is
   now content-addressed from the unit's name (`providerkit.UnitStageID`), which both sides
   derive independently. Worth promoting to a rule: a unit is named, not minted.
8. **A block header changed shape depending on when it flushed.** The header named its path
   only when the unit already had more than one child, so the same phase read `api` or
   `api › building` according to what had been declared by flush time.
9. **An enumerating planner that found nothing said "detail unavailable".** `RollUp` of an
   empty group returns UPDATE plus that reason — right for an engine-less provider that
   cannot enumerate, wrong for a preview that enumerated and found nothing to do.
10. **The result was emitted twice**, and **the failure block had no headline**. The second
    is the sharper one: with the stream as the product, a command does not get to write the
    closing line — the stream's `ResultEvent` is the conclusion.
11. **`✓ Deployed` for a run that deployed nothing.** The result vocabulary does not
    distinguish a plan from an apply; the session has to know it was `--dry` to say
    `Planned`.
12. **The projections diverge in content, not just presentation.** The build writer emits
    four empty lines; the renderer drops them from the block, and the NDJSON projection
    keeps them as four `{"log":{"message":""}}` records. Same for `degraded` and refusals,
    which reach JSON as presentation records rather than their own typed shapes. That
    contradicts [#619](https://github.com/ocelhq/ocel/issues/619)'s "NDJSON = protojson per
    line, first-class" — the projection is hand-written per envelope kind and should be
    generated from the proto.
13. **`deployui.truncateToWidth` counts escape sequences as columns** — confirmed on `main`,
    as #624 reported. Worth its own implementation issue.

## What the real path adds that the scripted one could not

`Reporter.Detail` carried no stage, so every provider log line was already headed for a
global gutter. A scripted stream never showed it, because the script assigned lines to
blocks by hand. Fixed here by stamping the reporting stage on `LogEvent`.

Also: a dry run declares nine stages and uses three — `preparing`, `uploading` and
`promoting` are announced and never happen. #624 left "empty blocks" open; this is that
question with a number on it.

## Still open

- The apply half (see above).
- The row budget for the live window under a real fan-out — this run had one app.
- Whether `promotion` and the edge deserve unit status.
- Whether stages a run will not reach should be declared at all (the nine-vs-three above).
- Whether the NDJSON projection should be generated from the proto (defect 12).

## Run it

```
make all
ocel preview up --dry --config ocel.proto630.config.ts --name proto630
OCEL_LIVE_PULUMI=1 go test ./pkg/providerkit/pulumi -run TestPreviewProducesPlanRows -v
```

The #624 driver still builds alongside it, for comparison:

```
go build -o /tmp/protoui ./cli/internal/cli/runui/protocmd
/tmp/protoui --variant=aws-container
```

## Known test fallout

`cli/internal/cli/deploy`'s suite fails on this branch: four tests, every one asserting the
old `deployui` vocabulary — the `Waiting`/`Ctrl-C` banner, `provisioning...` progress lines,
the typed `degraded`/`failed` JSON records. Replacing that renderer is the point of the
branch; rewriting the suite belongs to the implementation issue. Every other module passes.
