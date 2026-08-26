# Pulumi preview as a ChangePlan source

Research for [#615](https://github.com/ocelhq/ocel/issues/615), part of the map [#614](https://github.com/ocelhq/ocel/issues/614).

Every claim below is sourced from `pulumi/pulumi` at `master` unless a `@v3.146.0` ref is
given — that is the version `pkg/providerkit/pulumi/runtime.go` pins, and the last section
lists where `master` and the pinned build disagree.

## Verdict

Yes. A Pulumi preview yields one engine event per resource step, carrying the step's
operation, URN, type, and a per-property detailed diff. Turning those into
`contractv1.ChangeGroup`/`Change` rows is a pure mapping with no missing input.

Three things are not free:

- The preview **runs the program and talks to the providers**. It is not a config-only
  plan: the engine calls `Check`, `Diff`, and even `Create`/`Update` with a `preview` flag
  on every custom resource, and executes `Read` steps for real.
- `contractv1.Change` has **nowhere to put property detail**. A `--verbose` tier needs a
  proto change; the operation-level tier does not.
- A preview against unrefreshed state is a plan against Pulumi's memory of the cloud, not
  the cloud. Refresh is **off by default**, and turning it on converts the cheap local plan
  into a full provider `Read` sweep.

## Where the rows come from

`Stack.Preview` in the Automation API returns a `PreviewResult` with only three fields —
the raw stdout, the raw stderr, and a per-operation **count**:

```go
type PreviewResult struct {
	StdOut        string
	StdErr        string
	ChangeSummary map[apitype.OpType]int
}
```

(`sdk/go/auto/stack.go:1506`; populated at `stack.go:373-375` from the single
`SummaryEvent`.) Counts alone cannot fill a `ChangePlan`.

Resource-level rows come from the **event stream**, not the return value. `Stack.Preview`
always runs the CLI with `--event-log <tempfile>` and tails it, fanning every decoded
`apitype.EngineEvent` out to the caller's channels (`sdk/go/auto/stack.go:341-349`,
`tailLogs`/`watchFile` at `stack.go:1893-1970`). The caller opts in with
`optpreview.EventStreams(ch)` (`sdk/go/auto/optpreview/optpreview.go:112`) — the same
mechanism `pkg/providerkit/pulumi/adapter.go` already uses on `Up` and drains in
`trace.go`.

The event that matters is `ResourcePreEvent`:

```go
type ResourcePreEvent struct {
	Metadata StepEventMetadata `json:"metadata"`
	Planning bool              `json:"planning,omitempty"`
}
```

`Planning` is `true` for the preview pass and `false` for the execution pass
(`pkg/engine/events.go:605-613`, fed from the deployment's `DryRun`), which is how one
consumer distinguishes a plan from a live deploy on an otherwise identical stream.

`StepEventMetadata` is the whole payload (`sdk/go/common/apitype/events.go:224-248`):

```go
type StepEventMetadata struct {
	Op           OpType                  `json:"op"`
	URN          string                  `json:"urn"`
	Type         string                  `json:"type"`
	Old          *StepEventStateMetadata `json:"old"`
	New          *StepEventStateMetadata `json:"new"`
	Keys         []string                `json:"keys,omitempty"`
	Diffs        []string                `json:"diffs,omitempty"`
	DetailedDiff map[string]PropertyDiff `json:"detailedDiff"`
	Logical      bool                    `json:"logical,omitempty"`
	Provider     string                  `json:"provider"`
}
```

`Keys` are the properties forcing a replacement; `Diffs` are the keys that changed;
`DetailedDiff` is the structured per-path diff. `Logical` marks steps that correspond to a
statement in the program rather than an engine-synthesised sub-step — the natural filter
for a summary tier.

### The other surface: `pulumi preview --json`

The CLI's `--json` flag buffers the whole preview and prints one `PreviewDigest`
(`pkg/display/json.go:31-76`):

```go
type PreviewDigest struct {
	Config        map[string]string   `json:"config,omitempty"`
	Steps         []*PreviewStep      `json:"steps,omitempty"`
	Diagnostics   []PreviewDiagnostic `json:"diagnostics,omitempty"`
	Duration      time.Duration       `json:"duration,omitempty"`
	ChangeSummary ResourceChanges     `json:"changeSummary,omitempty"`
	MaybeCorrupt  bool                `json:"maybeCorrupt,omitempty"`
}

type PreviewStep struct {
	Op             StepOp                  `json:"op"`
	URN            resource.URN            `json:"urn"`
	Provider       string                  `json:"provider,omitempty"`
	OldState       *apitype.ResourceV3     `json:"oldState,omitempty"`
	NewState       *apitype.ResourceV3     `json:"newState,omitempty"`
	DiffReasons    []resource.PropertyKey  `json:"diffReasons,omitempty"`
	ReplaceReasons []resource.PropertyKey  `json:"replaceReasons,omitempty"`
	DetailedDiff   map[string]PropertyDiff `json:"detailedDiff"`
}
```

This carries slightly more than the event stream (full old/new `ResourceV3` state rather
than the trimmed `StepEventStateMetadata`), but it is a terminal-output format: the
Automation API does not pass `--json` to `preview` and does not parse a digest, so reaching
it means shelling out to the CLI ourselves and giving up the inline-program path the
adapter is built on. `--json` is buffered by default; `PULUMI_ENABLE_STREAMING_JSON_PREVIEW`
switches it to streaming events (`pkg/cmd/pulumi/operations/preview.go:761-763`).

**Use the event stream.** It is the same channel the adapter already consumes, it needs no
new process, and it loses nothing a `ChangePlan` can represent.

## Operation vocabulary

`apitype.OpType`, the exact strings on the wire
(`sdk/go/common/apitype/history.go:60-96`, identical at `@v3.146.0`):

| `OpType` | String | Meaning (verbatim from source) |
| --- | --- | --- |
| `OpSame` | `same` | nothing to do. |
| `OpCreate` | `create` | creating a new resource. |
| `OpUpdate` | `update` | updating an existing resource. |
| `OpDelete` | `delete` | deleting an existing resource. |
| `OpReplace` | `replace` | replacing a resource with a new one. |
| `OpCreateReplacement` | `create-replacement` | creating a new resource for a replacement. |
| `OpDeleteReplaced` | `delete-replaced` | deleting an existing resource after replacement. |
| `OpRead` | `read` | reading an existing resource. |
| `OpReadReplacement` | `read-replacement` | reading an existing resource for a replacement. |
| `OpRefresh` | `refresh` | refreshing an existing resource. |
| `OpReadDiscard` | `discard` | removing a resource that was read. |
| `OpDiscardReplaced` | `discard-replaced` | discarding a read resource that was replaced. |
| `OpRemovePendingReplace` | `remove-pending-replace` | removing a pending replace resource. |
| `OpImport` | `import` | import an existing resource. |
| `OpImportReplacement` | `import-replacement` | replace an existing resource |

The engine's own `display.StepOp` set (`pkg/resource/deploy/step.go:2235-2274`) is a
superset on `master`, adding `diff` and `extend-parameterize`. Neither has an `apitype`
constant, so an unrecognised string can reach the stream. **Map defensively**: a `default`
that drops the row silently is wrong; either skip with a logged reason or fold to
`ACTION_KEEP`.

`IsReplacementStep` (`step.go:2276-2283`) is the engine's own predicate for the
replacement family: `replace`, `create-replacement`, `delete-replaced`,
`read-replacement`, `discard-replaced`, `remove-pending-replace`, `import-replacement`.

## Mapping onto `contractv1.Change_Action`

Target enum (`proto/provider/contract/v1/contract.proto`):
`ACTION_UNSPECIFIED`, `ACTION_CREATE`, `ACTION_UPDATE`, `ACTION_REPLACE`, `ACTION_DELETE`,
`ACTION_DISABLE_THEN_DELETE`, `ACTION_KEEP`.

| `OpType` | `Change_Action` | Note |
| --- | --- | --- |
| `same` | `ACTION_KEEP` | Only emitted when `--show-sames` is set; otherwise absent. A plan that wants "left in place" rows must ask for them. |
| `create` | `ACTION_CREATE` | |
| `update` | `ACTION_UPDATE` | |
| `delete` | `ACTION_DELETE` | |
| `replace` | `ACTION_REPLACE` | The default rendering of a replacement — one row. |
| `create-replacement` | *drop* | Sub-step of `replace`. Only emitted with `--show-replacement-steps`; emitting all three double-counts. |
| `delete-replaced` | *drop* | Same. |
| `read` | `ACTION_KEEP` | A `read` is a lookup of an unmanaged resource, not a change. Only shown with `--show-reads`. |
| `read-replacement` | `ACTION_REPLACE` | The read half of a replacement of a read resource. |
| `refresh` | `ACTION_KEEP` | Not a change to the cloud — the engine reconciling state. Emitted only when refresh is on. See below. |
| `discard` | `ACTION_DELETE` | Drops a previously-read resource from state. Nothing in the cloud changes; a plan that says "delete" here overstates. Prefer `ACTION_KEEP` with a reason, or drop. |
| `discard-replaced` | *drop* | Replacement sub-step. |
| `remove-pending-replace` | *drop* | Engine bookkeeping for a resource left pending-replace by a failed run. Never user-meaningful. |
| `import` | `ACTION_CREATE` | Adopts an existing resource. Cloud-side nothing is created; `ACTION_KEEP` with an "adopting existing" reason reads truer if the row is shown at all. |
| `import-replacement` | `ACTION_REPLACE` | |

`ACTION_DISABLE_THEN_DELETE` has no Pulumi counterpart. It is an Ocel concept (a
two-phase teardown), so preview never produces it.

`ACTION_UNSPECIFIED` should never be produced — it is the proto zero value, and a row that
carries it is a mapping bug, not a state.

**Recommendation for the summary tier**: keep rows where `Logical == true` and the action
maps to one of `CREATE`/`UPDATE`/`REPLACE`/`DELETE`; everything else is engine detail.
`Change.reason` is the natural home for the replacement trigger — `Keys` joined, e.g.
`"replaced because bucket changed"` — and `Change.slow` for the resource types Ocel already
knows take minutes.

`ChangeGroup.kind`/`Change.kind` and `.name` have no direct Pulumi field. `StepEventMetadata.Type`
is the type token (`aws:s3/bucket:Bucket`), and the URN's last `::`-separated segment is the
resource name — `resourceNameFromURN` in `pkg/providerkit/pulumi/trace.go` already parses
exactly that. Raw type tokens leaking into a user-facing plan would be a regression against
the hand-written groups in `handlers_removal.go`, so the adapter needs a type-token →
human-kind table, and `providerkit.DetailUnavailable` stays the honest fallback for
anything unmapped.

## What preview requires

Preview is **not** a config-only plan. Concretely:

- **The program runs.** `Stack.Preview` starts a language-runtime server for the inline
  program and passes `--client=<addr>` (`sdk/go/auto/stack.go:311-320`) — the identical
  path `Up` takes. Every `pulumi.NewX` call in the Ocel program executes.
- **Built artifacts must exist.** Constructing a path-backed archive hashes it on the spot:
  `archive.FromPath` calls `EnsureHashWithWD` before returning
  (`sdk/go/common/resource/archive/archive.go:102-117`). A program that points an archive at
  a build output directory fails during preview if that directory is not there. Preview
  cannot skip the build.
- **Providers are loaded and called.** The engine calls `Check` with `AllowUnknowns` set
  from `DryRun` (`pkg/resource/deploy/step_generator.go:1271-1272`, `1924-1925`) and
  `Diff` for every existing resource. More surprising: `CreateStep.Apply` and
  `UpdateStep.Apply` invoke the provider's `Create`/`Update` RPC unconditionally, passing
  `Preview: s.deployment.opts.DryRun` (`pkg/resource/deploy/step.go:366-375`, `1018`). The
  gRPC contract makes that safe — `proto/pulumi/provider.proto:831-833`: *"True if and only
  if the request is being made as part of a preview/dry run, in which case the provider
  should not actually create the resource"* — but it means preview needs the provider
  plugins installed and, for most providers, valid credentials.
- **`read` steps really read.** `ReadStep.Apply` notes *"Unlike most steps, Read steps run
  during previews. The only time we can't run is if the ID we are given is unknown"*
  (`step.go:1314-1316`), then issues a live `prov.Read`. A preview does hit the cloud.
- **No state is written.** The backend builds no snapshot manager when the kind is
  `PreviewUpdate` or `DryRun` is set (`pkg/backend/diy/backend.go:1370-1376`). Preview
  cannot corrupt the checkpoint.

### Unknown outputs

Anything downstream of a resource that does not yet exist is *unknown*. Providers receive
type-tagged sentinel strings rather than values
(`sdk/go/common/resource/plugin/rpc.go:65-85`):

```
UnknownBoolValue    = "1c4a061d-8072-4f0a-a4cb-0ff528b18fe7"
UnknownNumberValue  = "3eeb2bf0-c639-47a8-9e75-3b44932eb421"
UnknownStringValue  = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"
UnknownArrayValue   = "6a19a0b0-7e62-4c92-b797-7f8e31da9cc2"
UnknownAssetValue   = "030794c1-ac77-496b-92df-f27374a8bd58"
UnknownArchiveValue = "e48ece36-62e2-4504-bad9-02848725956a"
UnknownObjectValue  = "dd056dcd-154b-4c76-9bd3-c8f88648b5ff"
```

Two consequences for a plan built from preview:

1. Any renderer that prints property values **must** recognise these sentinels and print
   something like `(known after deploy)`. Printing the raw GUID would be a visible bug.
2. On a first deploy the plan is shallow by nature: resources whose very existence depends
   on an unknown cannot be enumerated, so the row count under-reports. The plan is a lower
   bound, and the wording around it should not promise otherwise.

## Property-level detail

Available, and structured. `DetailedDiff` maps a property path to:

```go
type PropertyDiff struct {
	Kind      DiffKind `json:"diffKind"`
	InputDiff bool     `json:"inputDiff"`
}
```

with six kinds (`sdk/go/common/apitype/events.go:196-220`): `add`, `add-replace`,
`delete`, `delete-replace`, `update`, `update-replace`. The `-replace` suffix identifies
exactly which property forces the replacement, which is the answer to *"why is this being
replaced?"* without any guessing.

Values are in `Old.Inputs`/`Old.Outputs` and `New.Inputs`/`New.Outputs` on
`StepEventStateMetadata`, with a documented caveat: *"Secrets have filtered out, and large
assets have been replaced by hashes as applicable"* (`events.go:272-276`). So a verbose
tier can show before/after for ordinary properties but will show a hash where an archive
was, and nothing where a secret was — which is the correct behaviour anyway.

`DetailedDiff` is deliberately not `omitempty`, to distinguish "no diff" from "provider
returned no detailed diff" (`events.go:240-243`). Older providers return only the coarse
`Diffs []string`, so a verbose renderer needs both paths.

**Blocker for the verbose tier**: `contractv1.Change` is `{kind, name, action, reason,
slow}`. There is no field for a property path, an old value, a new value, or a diff kind.
A `deploy --dry --verbose` that shows per-property changes requires extending the proto —
e.g. a `repeated PropertyChange details` on `Change`. The operation-level tier needs no
proto change at all, which makes it the obvious first cut.

## Refresh interaction

**Refresh is off by default.** The `--refresh` flag's default value is the empty string,
not `false` — `NoOptDefVal = "true"` only means a bare `--refresh` implies `--refresh=true`
(`pkg/cmd/pulumi/operations/preview.go:774-776`). The published CLI reference rendering this
as `(default: "true")` is describing `NoOptDefVal`, not the default behaviour.
`getRefreshOption` resolves it (`pkg/cmd/pulumi/operations/io.go:119-140`): an explicit
flag wins, else `Pulumi.yaml`'s `options.refresh: always`, else — verbatim — *"the default
functionality right now is to always skip a refresh"*.

So a plain preview diffs the program against **the last checkpoint**, not against the
cloud. If something drifted out-of-band, the plan is wrong in the specific way that matters
most: it says `same` for a resource that a real deploy will then have to fix, or it says
`update` on properties that are already correct.

`optpreview.Refresh()` (`optpreview.go:126` @v3.146.0) adds `--refresh`. The cost is a
provider `Read` per resource in the stack, in parallel — the same work `pulumi refresh`
does — and it produces `refresh` step events in the stream alongside the plan steps. State
is still never written, because the `PreviewUpdate` kind skips the snapshot manager.

For Ocel this is a real product decision, not a flag: a `--dry` that lies about drift is
worse than no `--dry`. The adapter already gates refresh per operation
(`Config.Refresh func(ref providerkit.StackRef, op Op) bool`), so a third `Op` value for
the dry path fits the existing shape.

## Locking and latency

**Preview takes no lock on the DIY backend.** `diyBackend.Preview` delegates straight to
`backend.Preview` (`pkg/backend/diy/backend.go:1197-1201`), while `Update`, `Refresh`,
`Destroy`, and `Import` each call `b.Lock(ctx, stack.Ref())` first (`:1206`, `:1233`,
`:1258`, `:1283`). `backend.Preview` just sets `ApplierOptions{DryRun: true}` and applies
(`pkg/backend/apply.go:277-285`). Locking has been unconditional for real operations on
this backend since v3.20.0 (`changelog/v3.20.0.md:30-33`) — preview was never in that set.

Practical effect: a `deploy --dry` is safe to run concurrently with anything, and cannot
hit the `"the stack is currently locked"` path that `adapter.go`'s `busy()` converts into
`CodeBusy`. It also means a dry run cannot serialise against a concurrent real deploy; two
previews and an in-flight `up` will happily interleave, and the preview may read a
checkpoint that the `up` is mid-way through replacing.

**Latency** is not published as a number and depends entirely on the program and the
provider, but the components are all visible above and none are avoidable:

1. Plugin install on a cold cache — the adapter installs the pinned CLI under
   `~/.ocel/pulumi/<version>` once (`runtime.go`), but resource plugins download per
   provider/version.
2. Language-runtime startup plus a full run of the Ocel program.
3. One `Check` and one `Diff` per resource, plus a `Create`/`Update` RPC with `preview:
   true` per changed resource, at `--parallel` width (the adapter's `DefaultParallel = 64`).
4. Live `Read` for every `read` step.
5. With `--refresh`, a `Read` for every resource in the stack.

Steps 1-3 are the same work `up` does before it prompts, so **the floor on `deploy --dry`
is the time `deploy` already spends before its first mutation.** It is not free and should
not be sold as instant.

## Prior art: what Pulumi's own CLI renders

Pulumi runs *one* renderer over *one* event stream for both phases, and this is worth
copying rather than reinventing.

`pulumi up` is literally a preview followed by an update: `Backend.Update` calls
`PreviewThenPromptThenExecute` (`pkg/backend/diy/backend.go:1212`,
`pkg/backend/httpstate/backend.go:1503-1507`), which runs the full preview, prompts, and
then executes — unless `--skip-preview`. `pulumi preview` calls the same applier with
`DryRun: true` and stops. The events, the step metadata, and the display code are identical
between the two; the only differences are:

- **The verb.** `ActionLabel(kind, dryRun)` (`pkg/backend/apply.go:58-80`) returns
  `"Previewing " + previewText` when dry, else the present-continuous form —
  `"Previewing update"` vs `"Updating"`, `"Previewing destroy"` vs `"Destroying"`.
- **The `Planning` flag** on each resource event, `true` for the preview pass.
- **`SummaryEvent.IsPreview`** (`sdk/go/common/apitype/events.go:182-183`), which tells the
  footer whether to say what *would* happen or what *did*.

The per-row vocabulary is shared too — sigil and colour keyed only by `StepOp`
(`pkg/resource/deploy/step.go:2286-2372`):

| Op | Prefix | Colour role |
| --- | --- | --- |
| `same` | two spaces | unimportant |
| `create` | `+ ` | create |
| `delete` | `- ` | delete |
| `update` | `~ ` | update |
| `replace` | `+-` | replace |
| `create-replacement` | `++` | create-replacement |
| `delete-replaced` | `--` | delete-replaced |
| `read` | `> ` | read |
| `read-replacement` | `>>` | replace |
| `refresh` | `~ ` | update |
| `discard` | `< ` | delete |
| `discard-replaced` | `<<` | delete |
| `import` | `= ` | create |
| `import-replacement` | `=>` | replace |
| `remove-pending-replace` | `~ ` | unimportant |

Ocel's `cli/internal/changeplan/changeplan.go` already picked a compatible-but-distinct
set — `+` create, `~` update, `±` replace, `–` delete, blank keep — and it renders from
`*contractv1.ChangePlan`, so it is agnostic about where the plan came from. `Render` and
`Tally` work unchanged on a preview-derived plan. **The shared-renderer conclusion holds:
build the plan on the provider side, keep one CLI renderer, and let `--dry` differ from a
real deploy only in the header and footer wording** — exactly the split `ActionLabel` and
`IsPreview` encode.

## Pinned-version caveats

`pkg/providerkit/pulumi/runtime.go` pins `PinnedVersion = "3.146.0"`. Against that tag:

- `apitype.OpType` is the same 15 constants (`history.go@v3.146.0:68-96`), and
  `display.StepOp`'s extra `diff`/`extend-parameterize` are `master`-only. The defensive
  default is still worth writing.
- `optpreview` at v3.146.0 offers: `Parallel`, `Message`, `ExpectNoChanges`, `Diff`,
  `Replace`, `Target`, `TargetDependents`, `DebugLogging`, `ProgressStreams`,
  `ErrorProgressStreams`, `EventStreams`, `UserAgent`, `Color`, `Plan`, `Refresh`,
  `SuppressProgress`, `SuppressOutputs`, `ImportFile`, `AttachDebugger`, `ConfigFile`.
  **Not available**: `Exclude`, `ExcludeDependents`, `RunProgram`, `PolicyPacks`,
  `PolicyPackConfigs` — all `master` additions. Nothing on the critical path is missing;
  `EventStreams`, `Refresh`, and `Parallel` are all present.

## What is not answered here

- Whether `Change` gains property detail, or a verbose tier ships as free-form `reason`
  text. That is a proto decision, not a Pulumi fact.
- The type-token → human-kind table. It is Ocel-specific and depends on which resources the
  provider programs actually declare.
- Whether `--dry` refreshes. Costed above; the call is a product one.

## Sources

- `pulumi/pulumi` `sdk/go/auto/stack.go` — `Stack.Preview`, `PreviewResult`, `tailLogs`
- `pulumi/pulumi` `sdk/go/auto/optpreview/optpreview.go` (`master` and `v3.146.0`)
- `pulumi/pulumi` `sdk/go/common/apitype/events.go` — engine event union, step metadata, diff kinds
- `pulumi/pulumi` `sdk/go/common/apitype/history.go` — `OpType`
- `pulumi/pulumi` `sdk/go/common/resource/plugin/rpc.go` — unknown-value sentinels
- `pulumi/pulumi` `sdk/go/common/resource/archive/archive.go` — `FromPath` hashing
- `pulumi/pulumi` `pkg/resource/deploy/step.go` — `StepOp` set, prefixes, colours, `CreateStep`/`ReadStep` preview behaviour
- `pulumi/pulumi` `pkg/resource/deploy/step_generator.go` — `AllowUnknowns` under `DryRun`
- `pulumi/pulumi` `pkg/backend/apply.go` — `Preview`, `PreviewThenPromptThenExecute`, `ActionLabel`
- `pulumi/pulumi` `pkg/backend/diy/backend.go`, `pkg/backend/httpstate/backend.go` — locking, snapshot manager
- `pulumi/pulumi` `pkg/cmd/pulumi/operations/preview.go`, `pkg/cmd/pulumi/operations/io.go` — flags, `getRefreshOption`
- `pulumi/pulumi` `pkg/display/json.go` — `PreviewDigest`
- `pulumi/pulumi` `proto/pulumi/provider.proto` — `preview` on `CreateRequest`/`UpdateRequest`
- `pulumi/pulumi` `changelog/v3.20.0.md` — DIY backend locking
- https://www.pulumi.com/docs/iac/cli/commands/pulumi_preview/
- https://www.pulumi.com/docs/iac/cli/commands/pulumi_refresh/
