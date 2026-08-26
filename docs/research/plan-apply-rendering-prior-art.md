# Plan/apply rendering: prior art

Research for [#616](https://github.com/ocelhq/ocel/issues/616), part of map [#614](https://github.com/ocelhq/ocel/issues/614).

Question: how do Terraform, Pulumi and CDK present plan and apply, and what do they do about
TTY vs CI vs machine output? The decisive sub-question: **is the human view a projection of the
same typed event stream the machine view emits, or two code paths?**

Answer up front, one line each:

| Tool | Human view is… | Machine stream | Stability guarantee |
| --- | --- | --- | --- |
| Terraform | a **separate renderer** over the same `*plans.Plan` — not the `-json` stream | `-json` NDJSON, `terraform.ui` | versioned `ui` field, tied to the 1.0 compatibility promises |
| Pulumi | **a pure function of** the same typed event stream — provably, it can be replayed from the JSON | `--json` / `--event-log`, `apitype.EngineEvent` | none — explicitly *not* versioned, and the docs say nothing either way |
| CDK | a **listener on** the typed stream for `deploy`; the **only** renderer for `diff` | `IoMessage<T>` in `@aws-cdk/toolkit-lib` (no `cdk diff --json` at all) | written "code + data only" contract; text, level and emission order explicitly excluded |

---

## Terraform

### The answer: two renderers over one plan, and a *third* shared intermediate

Terraform's human view is **not** a projection of the `-json` event stream. They are two
independently written renderers fed from the same typed Go value (`*plans.Plan`). But the human
*plan diff* is a projection of a different shared intermediate — the `jsonplan` structured format,
which is also what `terraform show -json` emits.

`internal/command/views/` defines one interface per command with a `Human` and a `JSON`
implementation, selected by `arguments.ViewType`:

- [`views/plan.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/plan.go) —
  `type Plan interface { Operation() Operation; Hooks() []terraform.Hook; Diagnostics(...); HelpPrompt() }`,
  with `NewPlan(vt, view)` returning `&PlanJSON{...}` or `&PlanHuman{...}`.
- [`views/apply.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/apply.go) — `ApplyHuman` / `ApplyJSON`.
- [`views/operation.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/operation.go) —
  `type Operation interface { PlannedChange(*plans.ResourceInstanceChangeSrc); Plan(*plans.Plan, *terraform.Schemas); PlanNextStep(...); ... }`.

The asymmetry gives it away: `NewOperation` only knows how to build the human one and `panic`s
otherwise; `PlanJSON.Operation()` constructs `&OperationJSON{}` directly. And the two `Plan()`
bodies are disjoint —

```go
// OperationHuman.Plan
jsonPlan, err := jsonplan.MarshalForRenderer(plan, schemas)
renderer := jsonformat.Renderer{Colorize: v.view.colorize, Streams: v.view.streams, RunningInAutomation: v.inAutomation}
renderer.RenderHumanPlan(jplan, plan.UIMode, opts...)
```

`OperationJSON.Plan` never touches `jsonformat`. It walks `plan.DriftedResources`,
`plan.Changes.Resources`, `plan.Changes.Outputs`, `plan.DeferredResources` and emits
`ResourceDrift`, `PlannedChange`, `ChangeSummary`, `Outputs`, `DeferredChange` — with its own
`switch change.Action` tally loop. So the `Plan: N to add…` line and the `change_summary` counts
are **two separate counting implementations over the same `plans.Changes`**. Likewise `UiHook` and
`jsonHook` are two independent `terraform.Hook` implementations
([`hook_ui.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/hook_ui.go),
[`hook_json.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/hook_json.go)),
each with its own goroutine, message strings and elapsed-time formatting.

The human side declines the per-instance event outright — `OperationHuman.PlannedChange` is an
empty method: *"PlannedChange is primarily for machine-readable output… We don't use it with
OperationHuman because the output of Plan already includes the change details for all resource
instances."*

**What is actually shared:**

1. **`jsonplan` + `jsonformat`.** This is the real shared intermediate, and it sits between the
   *human plan diff* and *`terraform show -json`* — not between human and the streaming `-json`.
   [`jsonformat/README.md`](https://github.com/hashicorp/terraform/blob/main/internal/command/jsonformat/README.md):
   *"The renderer accepts the JSON structured output produced by the `terraform show <plan-file>
   -json` command and writes it in a human-readable format."* The pipeline is `jsonplan.Change` →
   `structured.Change` → `differ` → `computed.Diff` → `Diff.RenderHuman()`, with the stated intent
   *"to ensure the process by which the plan difference is calculated is separated from the
   rendering itself… it should be possible to modify the rendering or add new renderer formats
   without being concerned with the complex diff calculations."* The README concedes only one
   renderer exists today. `views/show.go` makes the same call, and also decodes HCP Terraform's
   *redacted* plan JSON straight into a `jsonformat.Plan` — so a remote plan and a local plan reach
   the same human renderer.
2. **`countHook`** — one instance handed to both view flavours.
3. **`format.DiffActionSymbol`**, in a package whose doc-comment says it is *"exported to encourage
   non-official frontends to mimic the output formatting"*
   ([`internal/command/format/format.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/format/format.go)).
4. One genuine JSON→human projection does exist, but as a *consumer*, not the CLI's own path:
   `Renderer.RenderLog(log *JSONLog)`
   ([`jsonformat/renderer.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/jsonformat/renderer.go))
   renders NDJSON UI messages back to human text for `terraform test` and remote-run logs — and it
   *drops* `LogPlannedChange`, `LogRefreshComplete`, `LogVersion`, `LogApplyErrored` and others
   (*"We won't display these types of logs"*), precisely because the structured renderer generates
   the plan summary itself.

**Consequence:** you cannot reconstruct the human plan diff from the `-json` stream.
`planned_change` carries address, action and reason — no attribute-level before/after. The
attribute diff exists only in the `jsonplan` format.

### Machine-readable UI

Docs: <https://developer.hashicorp.com/terraform/internals/machine-readable-ui>. The framing is
blunt:

> By default, many Terraform commands display UI output as unstructured text that is intended to be
> read in a terminal emulator. **This text stream is not a stable interface for integrations.** Some
> commands support a `-json` flag, which enables a structured JSON output mode with a defined
> interface.

Envelope: `@level`, `@message`, `@module` (always `"terraform.ui"`), `@timestamp` (RFC3339), `type`.
Implementation is just hclog in JSON mode —
[`views/json_view.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/json_view.go):
`hclog.New(&hclog.LoggerOptions{Name: "terraform.ui", Output: view.streams.Stdout.File, JSONFormat: true})`,
with the note that *"The logger has an internal mutex to ensure that messages are not interleaved."*

**Stability guarantee — versioned, and an explicit promise:**

> The first message output has type `version`, and includes a `ui` key… The semantics of this
> version are:
> - We will increment the minor version… for backward-compatible changes or additions. **Ignore any
>   object properties with unrecognized names to remain forward-compatible with future minor
>   versions.**
> - We will increment the major version… for changes that are not backward-compatible. **Reject any
>   input which reports an unsupported major version.**

Linked to the [Terraform 1.0 Compatibility Promises](https://developer.hashicorp.com/terraform/language/v1-compatibility-promises).
Nothing marks it experimental. Also: *"Clients presenting the logs as a user interface should handle
unexpected message types by presenting at least the `@message` field to the user."*

Doc drift worth knowing: the prose still says `"1.0"`, but the source constant is
`JSON_UI_VERSION = "1.3"` (`json_view.go`), commented *"This version must be updated after making
any changes to this view, the jsonHook, or any of the command/views/json package."* Treat the
constant as authoritative.

Message types are enumerated in
[`views/json/message_types.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/views/json/message_types.go):
`version`, `log`, `diagnostic`, `resource_drift`, `planned_change`, `deferred_change`,
`change_summary`, `outputs`, `apply_start`/`_progress`/`_complete`/`_errored`,
`provision_*`, `refresh_start`/`_complete`, `ephemeral_op_*`, `test_*`, `list_*`, `action_*`,
`policy_*`, provider-installation and state-migration types. The **source list is a strict superset
of the documented list** — which is exactly why the "handle unexpected types via `@message`"
instruction exists.

Flag semantics, all verified in `internal/command/arguments/`:

- **`-json` does not imply `-no-color`.** Neither `arguments/plan.go` nor `arguments/apply.go`
  touches `NoColor` when `json` is set. It is moot in practice — the JSON view writes via hclog and
  never consults `colorize`.
- **`-json` does imply `-input=false`** (`// JSON view currently does not support input, so we
  disable it here`), and the docs say so.
- **`apply -json` additionally requires `-auto-approve` or a saved plan**, as a hard diagnostic:
  *"Terraform cannot ask for interactive approval when -json is set…"*, with the comment *"We
  intentionally fail here rather than override auto-approve, which would be dangerous."*
- Curiosity: `-json` is absent from `terraform plan -help`; it exists only on the website.

### Human plan conventions

Two sigil tables, both in shared code. The legend
([`jsonformat/plan.go`](https://github.com/hashicorp/terraform/blob/main/internal/command/jsonformat/plan.go),
`actionDescription()`) prints only the actions actually present in the plan, under *"Resource
actions are indicated with the following symbols:"* — `+ create`, `- destroy`, `~ update in-place`,
`+/- create replacement and then destroy`, `-/+ destroy and then create replacement`,
`<= read (data resources)`.

Per-line symbols come from `format.DiffActionSymbol`, always padded to three cells: `"  +"`,
`"  -"`, `"  ~"`, `" <="`, `"-/+"`, `"+/-"`, `" ."` (forget), `" +/."`, `"   "` (no-op), `"  ?"`.
Suppressible via `opts.HideDiffActionSymbols` — which is what `RenderHumanState` does for
`terraform show`. Note `+/-` and `-/+` are not a stylistic pair: `CreateThenDelete` is `+/-`
(`create_before_destroy`), `DeleteThenCreate` is `-/+`.

The tally:

```go
buf.WriteString(fmt.Sprintf("%d to add, %d to change, %d to destroy.",
    counts[Create]+counts[DeleteThenCreate]+counts[CreateThenDelete]+counts[CreateThenForget],
    counts[Update],
    counts[Delete]+counts[DeleteThenCreate]+counts[CreateThenDelete]))
```

A replacement counts **once in add and once in destroy**. Apply's counterpart is
`"Apply complete! Resources: %d added, %d changed, %d destroyed."`

Headers come from `resourceChangeComment()` — a bold, two-space-indented `# <addr> …` line with a
large vocabulary: `will be created`, `will be read during apply`, `will be updated in-place`,
`has changed` (drift), `is tainted, so must be replaced`, `will be replaced, as requested`,
`will be replaced due to changes in replace_triggered_by`, `must be replaced`, `will be destroyed`,
`has been deleted`, `has moved to <addr>`, `will be imported`,
`will be removed from Terraform state, but will not be destroyed`. Plus parenthetical reason lines
(`# (config refers to values not yet known)`, `# (because index [N] is out of range for count)`,
`# (moved from <addr>)`, …).

Redaction and elision:

- `(sensitive value)` — `computed/renderers/sensitive.go`. The differ never holds the real value:
  the README notes sensitive values are obfuscated as generic JSON *"as we never need to print them
  anyway."* It also emits a transition warning when an attribute *becomes* sensitive.
- `(known after apply)` — bare for creates, `"%s -> (known after apply)"` when a known before-value
  exists.
- `# forces replacement` in red, driven by propagating `jsonplan.Change.ReplacePaths` down the
  value tree.
- `# (N unchanged attributes hidden)` in dark grey, suppressed under `opts.ShowUnchangedChildren`
  (set for imports and for `terraform show`).
- Nesting is 4 spaces per level, and **parents own their children's presentation** — README:
  *"the render function itself doesn't print out metadata about its own change (eg. there's no `~`
  symbol in front of the opening bracket)."*

Empty-plan strings are distinct per mode: `No changes. Your infrastructure matches the
configuration.` / `…still matches the configuration.` (refresh-only) / `No changes. No objects need
to be destroyed.` / `Planning failed…` / `No current changes. This plan requires another plan to be
applied first.`

`RenderHumanPlan` also prints a version-skew banner when the plan's format version exceeds the
renderer's: renderer-newer-than-plan is fine, plan-newer-than-renderer is not.

### Non-TTY: nothing degrades, because nothing redraws

Terraform's plan/apply UI is strictly line-per-event and append-only. `UiHook` writes exclusively
through `h.println()` → `h.view.streams.Println(s)`. **No spinners, no `\r`, no ANSI cursor
movement, no redraw.** (The only `\r` handling is `scanLines`, splitting inbound *provisioner*
output.) `terminal.OutputStream` exposes only `File`, `Columns()`, `IsTerminal()` — there is no
cursor API to call. The human view never branches on `IsTerminal()`; across the repo `IsTerminal` is
used in `internal/terminal/*`, `main.go` (TRACE logging), `ui_input.go` and `meta.go` (an *input*
decision), not in `views/`.

Width detection is the one thing that changes: `View.outputColumns()` → `streams.Stdout.Columns()`,
backed by `golang.org/x/term.GetSize`, falling back to `const defaultColumns int = 78` on error or
non-terminal. Piping gives prose word-wrapped at 78 columns and 78-char horizontal rules. Width is
read at call time, so it does not react to a mid-run resize.

Long-running progress, human: `PreApply` prints `<addr>: Creating...` and spawns
`go h.stillRunning(uiState)`, which loops on `time.After(h.periodicUiTimer)` until `DoneCh` closes:

```go
"[reset][bold]%s: %s [%s%02dm%02ds elapsed][reset]"   // addr, "Still creating...", idSuffix, m, s
```

Interval is `const defaultPeriodicUiTimer = 10 * time.Second`, field-injectable for tests. IDs are
truncated to 80 runes with a middle ellipsis. `PostApply` prints
`Creation complete after 1m30s [id=…]`.

The JSON side mirrors it with a separate implementation: `jsonHook.PreApply` emits `apply_start`
then `go h.applyingHeartbeat(progress)`, emitting `apply_progress` on the **same 10s timer** with
`elapsed_seconds`. `time.Now`/`time.After` are struct fields — *"Mockable functions for testing the
progress timer goroutine"*. `plans.NoOp` resources get no start message and no heartbeat.

Parallel resources are **interleaved, address-prefixed lines** — no tree, no per-resource region.
Every line is self-identifying because it starts with the resource address. Ordering is
nondeterministic under the default `-parallelism=10`; atomicity is guaranteed only per line (a
`viewLock` mutex on the human side — *"Wrap calls to the view so that concurrent calls do not
interleave println"* — and hclog's internal mutex on the JSON side). The docs say so outright:

> Messages will be emitted as events occur to trigger them. This means that messages related to
> several resources may be interleaved… The `resource` object value can be used to link multiple
> messages about a single resource.

### `NO_COLOR` — not honoured, and no TTY detection either

A code search over `hashicorp/terraform` for `NO_COLOR` returns **zero results**, and it is absent
from the [environment variables page](https://developer.hashicorp.com/terraform/cli/config/environment-variables).
The workaround is `TF_CLI_ARGS="-no-color"`.

Colour is **on by default and stays on when piped**. `Color: true` is a literal in `commands.go`,
and only the `-no-color` flag flips it: `arguments.ParseView` strips `-no-color` from argv before
the per-command flag set runs (so it works in any position on every command), and
`View.Configure` sets `v.colorize.Disable = view.NoColor`. Colorization is
[`mitchellh/colorstring`](https://github.com/mitchellh/colorstring) — inline `[bold][green]…[reset]`
markers that `Colorize.Color()` either expands or strips — not a TTY-aware library. So
`terraform plan | cat` emits raw ANSI, and **`-no-color` is mandatory in CI logs**. What *does*
auto-detect: terminal width, and stdin interactivity for prompts. That is all.

`TF_CLI_ARGS` / `TF_CLI_ARGS_<cmd>` are merged in `main.go`, parsed with `shellwords`, and inserted
*after* the subcommand and *before* command-line flags — which is what lets explicit flags win.

`TF_IN_AUTOMATION` (any non-empty value) is threaded into `views.NewView(...).SetRunningInAutomation(...)`.
Its effects are **cosmetic only** — the docs say *"only affects cosmetic changes to human-readable
output"*, and the code comment reads *"This is a hint not to produce messages that expect that a
user can run a follow-up command."* Concretely it suppresses the `terraform apply "tfplan"`
next-step block and the `terraform apply -refresh-only` suggestion. It does not touch colour,
hooks or progress.

### Caveats

- The exact release where the human elapsed format changed from `[10s elapsed]` to
  `[0m10s elapsed]` was not bisected — only that v1.5.0 had the old form and v1.13.0/main have the
  new one.
- `jsonplan.Marshal` vs `MarshalForRenderer` were not diffed to enumerate what the renderer-oriented
  variant omits.
- The `NO_COLOR` search covers the default branch only, and vendored dependencies were not audited
  (`colorstring` itself does not handle it).

---

## AWS CDK

CDK is two different answers in one CLI, and the split is the most useful thing in it.

Repo note: the code has moved out of `aws/aws-cdk` into **`aws/aws-cdk-cli`**. Diff formatting
now lives in `@aws-cdk/toolkit-lib`; `cdk-toolkit.ts` survives at
`packages/aws-cdk/lib/cli/cdk-toolkit.ts`.

### The typed layer is real

[`packages/@aws-cdk/toolkit-lib/lib/api/io/io-message.ts`](https://github.com/aws/aws-cdk-cli/blob/main/packages/%40aws-cdk/toolkit-lib/lib/api/io/io-message.ts)
defines `IoMessage<T>` — `time`, `level`, `action`, `code`, `message`, `span`, `data: T` — with
`IoMessageCode` typed as `` `CDK_${string}_${'E'|'W'|'I'}${number}${number}${number}${number}` ``.
The doc-comment on `level` states the design directly: *"This is an indicative level and should
not be used to explicitly match messages, instead match the `code`. The level of a message may
change without notice."*

### `deploy` — the good prior art: two printers over one typed stream

`packages/@aws-cdk/toolkit-lib/lib/api/stack-events/stack-activity-monitor.ts` emits exactly
three coded messages: `CDK_TOOLKIT_I5501` (start), `I5502` (each activity), `I5503` (stop).
The payload is genuinely structural —
`packages/@aws-cdk/toolkit-lib/lib/payloads/stack-activity.ts`:

```ts
export interface StackActivity {
  readonly deployment: string;   // UUID; disambiguates concurrent stacks
  readonly event: StackEvent;    // raw CloudFormation event
  readonly metadata?: ResourceMetadata;  // construct path, from the cloud assembly
  readonly progress: StackProgress;      // { total?, completed, formatted: "[34/42]" }
}
```

`deployment` is the parallelism key: several stacks deploy concurrently and every event carries
the UUID of the deployment it belongs to, so a renderer can group rather than interleave.

The renderers live in `packages/@aws-cdk/toolkit-lib/lib/private/activity-printer/` and are pure
consumers — `base.ts` declares `interface IActivityPrinter { notify(msg: IoMessage<unknown>): void }`
and `ActivityPrinterBase.notify` is a bare code switch (`I5501.is(msg)` → `start`, `I5502.is` →
`activity`, `I5503.is` → `stop`, default ignore):

- `current.ts` — `CurrentActivityPrinter`, TTY: redraws a block via `RewritableBlock`, showing a
  progress bar and only the resources currently in flight.
- `history.ts` — `HistoryActivityPrinter`, non-TTY: one line per event, buffered; after
  `inProgressDelay = 30_000` ms of silence it prints what is still in progress, and on `stop()`
  re-prints a "Failed resources:" recap.
- `errors-only.ts` — `ErrorsOnlyActivityPrinter`.

The CLI wires the human view in as *a listener*, in
`packages/aws-cdk/lib/cli/io-host/cli-io-host.ts` (`routeStackActivityToPrinter`):

```ts
this.addMessageListener({
  once: false, internal: true, fn: route,   // route: this.activityPrinter.notify(msg); return { preventDefault: true };
  matches: matchAny(IO.CDK_TOOLKIT_I5501, IO.CDK_TOOLKIT_I5502, IO.CDK_TOOLKIT_I5503),
});
```

That is the shape worth copying: the stream is primary, the human renderer is one subscriber, and
a machine consumer registers a different subscriber. Note the printers are under `lib/private/` —
they are not public API even though the messages are.

### `diff` — the anti-pattern

There is no `cdk diff --json`. `packages/aws-cdk/lib/cli/cli-config.ts` defines the `diff` command
with `context-lines`, `exclusively`, `security-only`, `fail`, `quiet`, `change-set`, `method`,
`template`, `strict`, `processed`, `include-moves` — and no `json`. The
[docs page](https://docs.aws.amazon.com/cdk/v2/guide/ref-cli-cmd-diff.html) lists the same set.
The *global* `--json` is unrelated: cli-config.ts describes it as *"Use JSON output instead of YAML
when templates are printed to STDOUT"*, and it only reaches `serializeStructure(obj, json)` for
`synth`/`metadata`/`list`.

Worse, the structure and the rendered text ship in the same message.
`packages/@aws-cdk/toolkit-lib/lib/payloads/diff.ts`:

```ts
export interface StackDiff extends SingleStack {
  readonly diffs: { [name: string]: ITemplateDiff };  // structural
  readonly formattedDiff: FormattedDiff;              // pre-rendered string
  ...
}
```

`DiffFormatter` (`lib/api/diff/diff-formatter.ts`) renders producer-side into a
`StringWriteStream` via `formatDifferences(stream, activeDiff, {...})`, and
`Toolkit.diff()` emits `IO.CDK_TOOLKIT_I4002.msg(stackDiff.formattedDiff, {...})` — the message
*text* is the entire rendered diff.

And the "structural" half is not actually serializable.
`packages/@aws-cdk/cloudformation-diff/lib/diff/types.ts` declares
`class TemplateDiff` and `class DifferenceCollection<V, T>` — class instances wrapping private
fields and exposing getters (`changes`, `differenceCount`, `get()`, `remove()`), not plain records.
It is an in-process API for Node integrators, not a wire format. `formatDifferences`
(`packages/@aws-cdk/cloudformation-diff/lib/format.ts`, `class Formatter`) is the only consumer of
`TemplateDiff` in the repo; the CLI's `CdkToolkit.diff()` just does
`this.ioHost.asIoHelper().defaults.info(diff.formattedDiff)` and reads `numStacksWithChanges` for
the exit code.

### Non-TTY / CI degradation

`--ci` is a **global** flag, defaulting to an environment sniff —
`packages/aws-cdk/lib/cli/cli-config.ts`: *"Force CI detection. If CI=true then logs will be sent
to stdout instead of stderr"*, `default: YARGS_HELPERS.isCI()`, where
`packages/aws-cdk/lib/cli/util/ci.ts` is `process.env.CI !== undefined && !== 'false' && !== '0'`.

Stream routing is **level-based, not mode-based** — `CliIoHost.selectStreamFromLevel`, whose own
comment reads:

```
//   (1) Messages of level `result` always go to `stdout`
//   (2) Messages of level `error` always go to `stderr`.
//   (3a) All remaining messages go to `stderr`.
//   (3b) If we are in CI mode, all remaining messages go to `stdout`.
```

So `cdk synth > file.yaml` keeps working in every mode. Prompting is gated separately, on TTY, not
on CI: `CliIoHost` cannot prompt when `!this.isTTY` and falls back to the request's
`defaultResponse`.

CDK also splits CI into a *third* question — whether stderr is safe on this particular CI system.
`packages/aws-cdk/lib/cli/ci-systems.ts` enumerates Azure DevOps, TeamCity, GitHub Actions,
CodeBuild, CircleCI and Jenkins with a `canBeConfiguredToFailOnStdErr` flag, and `cli.ts` gates
notices on `const isSafeToWriteNotices = !isCI() || Boolean(ciSystemIsStdErrSafe());`.

### Progress selection is a cascade, and has three modes

`CliIoHost.get stackProgress()`, in order:

1. explicit `EVENTS` or `ERRORS_ONLY` — always honoured;
2. debug-level logging enabled → force `EVENTS` (redrawing fights interleaved logs);
3. `process.platform === 'win32'` → force `EVENTS` (*"On Windows we cannot use fancy output"*);
4. `const fancyOutputAvailable = this.isTTY && !this.isCI;` → otherwise force `EVENTS`. The comment
   cites CircleCI specifically: *"On some CI systems (such as CircleCI) output still reports as a
   TTY so we also need an individual check for whether we're running on CI."*
5. otherwise the user preference (`BAR`).

`StackActivityProgress` (`packages/aws-cdk/lib/commands/deploy.ts`) has three values — `bar`,
`events`, and `errors-only`, the last documented in source as *"Recommended for AI agents and other
consumers that don't benefit from continuous progress updates."* The
[AWS docs are stale](https://docs.aws.amazon.com/cdk/v2/guide/ref-cli-cmd-deploy.html) and list
only `bar` and `events`. Settable in `cdk.json` as `{ "progress": "events" }`; precedence is
CLI > project `cdk.json` > `~/.cdk.json`. There is no `CDK_PROGRESS` env var (code search over
`aws/aws-cdk-cli` returns zero hits).

Undocumented but pointed: `packages/aws-cdk/lib/cli/cli.ts` auto-selects `errors-only` when it
thinks an agent is driving —

```ts
// Progress updates are wasted tokens for AI agents
if (guessAgent() && !argv.verbose && configuration.settings.get(['progress']) === undefined) {
  ioHost.stackProgress = StackActivityProgress.ERRORS_ONLY;
```

`packages/aws-cdk/lib/cli/util/guess-agent.ts` sniffs `CLAUDECODE`, `CODEX_THREAD_ID`,
`CURSOR_AGENT`, `GEMINI_CLI`, `COPILOT_CLI` and friends, returns `true | undefined` (*"It's hard
for us to say `false` for sure"*), and its doc-comment draws the line at *"variables set by the
agent itself… Variables that merely indicate an agent-capable IDE or environment… do not: a human
typing in such a terminal would be misdetected."*

### Colour

CDK never reads `NO_COLOR` in product code — a code search over `aws/aws-cdk-cli` returns one hit,
in a test fixture. `NO_COLOR` works only because `chalk` reads it. CDK's own control is
`FORCE_COLOR`, set from flags in `cli.ts`:

```ts
// Priority: --no-color > --color > TTY detection
if (argv.noColor) { process.env.FORCE_COLOR = '0'; }
else if (argv.color) { process.env.FORCE_COLOR = '3'; }
else if (!process.stdout.isTTY) { process.env.FORCE_COLOR = '0'; }
```

Because `supports-color` gives `FORCE_COLOR` precedence over `NO_COLOR`, `cdk --color` overrides a
user's `NO_COLOR`. A second, independent gate exists in `CliIoHost.formatMessage`:
`message_text = this.isTTY ? styleMap[msg.level](msg.message) : msg.message;` — but that covers
only the envelope; the diff body and the activity printers call `chalk` directly and depend on
chalk's global level.

**Unverified:** `chalk@4` resolves its level at module load, and `import chalk` sits at the top of
`cli.ts` while the `FORCE_COLOR` assignment happens later inside `exec()`. CDK's own tests
(`test/cli/cli.test.ts`, `describe('color output flag tests')`) only assert the env var value, not
the resulting colorisation. Whether `--color`/`--no-color` actually take effect depends on bundling
and module-init order, which could not be settled from source alone.

### Stability guarantees

`@aws-cdk/toolkit-lib` is GA (announced
[May 2025](https://aws.amazon.com/about-aws/whats-new/2025/05/aws-cdk-toolkit-library-available/);
tracking issue [aws/aws-cdk-cli#155](https://github.com/aws/aws-cdk-cli/issues/155)), and its
message registry carries the single best artefact found in this research —
`packages/@aws-cdk/toolkit-lib/docs/message-registry.md`, §"Backwards compatibility", rendered at
<https://docs.aws.amazon.com/cdk/api/toolkit-lib/message-registry/>:

> **Depend only on messages and requests with a `code`. Treat all other messages as informational
> only.** If a message does not have a code, it can change or disappear at any time without notice.
>
> **Only the `code` and `data` properties of a message are in scope for backwards compatibility.**
> Payload data can change, but we will only make type-compatible, additive changes.
>
> For the avoidance of doubt, the following changes are explicitly not considered breaking:
> - a change to the message text or level,
> - a change to the default response of a request,
> - a change to the order messages and requests are emitted in,
> - the addition of new messages and requests, and
> - the removal of messages without a code

By contrast, AWS documents **no** machine-readable diff output and makes **no** stability promise
about the rendered diff. The `[+]`/`[-]`/`[~]` symbols are described illustratively on the docs
page, not committed to.

---

## Pulumi

Sources are `pulumi/pulumi@master`, pinned at `08d8a480194d5e81661e6d8f4f8c940ed8d5b7ae`. Line numbers
drift; symbol names do not.

### The answer: one stream, many renderers — and it is provable

There is exactly one `<-chan engine.Event` and exactly one fan-out point. The animated tree, the
rich diff, the JSONL stream, the preview digest, the summary line and the event log all hang off it.
`pkg/backend/display/display.go`, `ShowEvents`:

```go
stampedEvents := stampEvents(events)
if opts.EventLogPath != "" { stampedEvents, done = startEventLogger(stampedEvents, done, opts) }
stampedEvents = channel.FilterRead(stampedEvents, func(e engine.StampedEvent) bool { return !e.Internal() })
if opts.JSONDisplay {
    if isPreview && !streamPreview { ShowPreviewDigest(rawEvents, done, opts) } else { ShowJSONEvents(stampedEvents, done, opts) }
    return
}
if opts.SummaryJSON { ... tapSummaryJSON(rawEvents, opts) ... return }
switch opts.Type {
case DisplayDiff:     ShowDiffEvents(...)
case DisplayProgress: ShowProgressEvents(...)
case DisplayWatch:    ShowWatchEvents(...)
}
```

The typed union is `pkg/engine/events.go`. `EventPayload` is a Go 1.18 **type-set constraint** on the
generic constructor `NewEvent[T EventPayload](payload T)`, not a sum type — the struct field is
`payload any` and the discriminant is the `EventType` string (`"resource-pre"`,
`"resource-outputs"`, `"diag"`, `"prelude"`, `"summary"`, `"progress"`, …, 17 in all). `NewEvent`
`deepcopy.Copy`s the payload and locks `*resource.State` during construction, so events are
immutable snapshots that are safe to fan out.

`pkg/backend/display/events.go`, `ConvertEngineEvent`, maps `engine.Event` →
`apitype.EngineEvent`. The mapping is **structurally total** — all 17 event types have a case, and
`apitype.EngineEvent` has one pointer field per type (`json:"cancelEvent,omitempty"` and friends), a
1:1 discriminated union. It is lossy in two bounded, deliberate ways, both flagged in the doc-comment
(*"this operation is inherently lossy"*):

- **Secrets are blinded unconditionally.** `convertStepEventStateMetadata` hardcodes
  `config.BlindingCrypter`, and `logJSONEvent` calls
  `ConvertEngineEvent(event.Event, false /* showSecrets */)` — always false, for both `--json` and
  `--event-log`. So secret values never reach machine output even when the human view would show
  them under `--show-secrets`.
- **ANSI codes are stripped from diagnostics** via
  `regexp.MustCompile("\x1b\\[[0-9;]*[mK]")`, applied to `DiagEventPayload.Message` only.

**The proof the projection is faithful enough to invert.** Two independent code paths reconstruct
the *human display* from the *machine JSON* via `ConvertJSONEvent`, the inverse in the same file:

- `pulumi replay-events` (`pkg/cmd/pulumi/events/replay.go`) reads a JSONL `--event-log` file,
  decodes into `apitype.EngineEvent`, converts back, pushes onto a `chan engine.Event`, and calls
  **the same `display.ShowEvents`**. Its help text: *"This command loads events from the indicated
  file and renders them using either the progress view or the diff view."* Hidden behind
  `env.DebugCommands`.
- Pulumi Cloud–driven updates (`pkg/backend/httpstate/backend.go`): when the update runs
  server-side, the CLI polls `GetUpdateEngineEvents`, converts each event, and feeds the very same
  `ShowEvents` goroutine.

So Pulumi's human renderer is not merely *a* projection of the typed stream — it is a **pure
function of it**, demonstrably replayable from the serialized form. This is the strongest version of
the pattern found in any of the three tools.

### The renderer inventory (smaller than folklore suggests)

`pkg/backend/display/options.go`:

```go
const (
	DisplayProgress Type = iota  // displays an update as it progresses
	DisplayDiff                  // displays a rich diff
	DisplayWatch                 // displays watch output
)
```

- **There is no `DisplayJSON`** — JSON is a bool (`Options.JSONDisplay`), checked *before* the
  `switch opts.Type`, so it short-circuits whatever `Type` says. A second bool `SummaryJSON` backs
  `--output json`.
- **There is no `DisplayTree` and no `--tree` flag.** The tree is the *interactive renderer for*
  `DisplayProgress`, chosen by TTY-ness.
- **`DisplayQuery` has been deleted** since v3.100.0. `query.go` still defines `ShowQueryEvents`, but
  nothing dispatches to it.

Defaults are identical across `up`, `preview`, `destroy`, `refresh`, `import` and independent of
TTY/CI:

```go
displayType := display.DisplayProgress
if diffDisplay { displayType = display.DisplayDiff }
```

`watch` hardcodes `DisplayWatch` and `IsInteractive: false`. **CI does not change the display type** —
no `ciutil` import exists under `pkg/backend/display/`.

Under `DisplayProgress` there are three renderers, all satisfying the `progressRenderer` interface
(`initializeDisplay`, `tick`, `rowUpdated`, `systemMessage`, `progress`, `done`, `println`, `Close`):

| Renderer | Constructor | File | When |
| --- | --- | --- | --- |
| `treeRenderer` | `newInteractiveRenderer` | `tree.go` | interactive + raw terminal |
| `messageRenderer` (interactive) | `newInteractiveMessageRenderer` | `jsonmessage.go` | interactive, non-raw — the Windows fallback |
| `messageRenderer` (non-interactive) | `newNonInteractiveRenderer` | `jsonmessage.go` | not interactive |

```go
func newInteractiveRenderer(term terminal.Terminal, permalink string, opts Options) progressRenderer {
	// Something about the tree renderer--possibly the raw terminal--does not yet play well with Windows, so for now
	// we fall back to the legacy renderer on that platform.
	if !term.IsRaw() { return newInteractiveMessageRenderer(term, opts) }
	term.HideCursor()
	...
}
```

### Machine output: four modes off the same channel

1. **`--json` on `up`/`destroy`/`refresh`** → newline-delimited `apitype.EngineEvent`.
   `ShowJSONEvents` uses `json.NewEncoder(os.Stdout)` with `SetEscapeHTML(false)`.
2. **`--json` on `preview`** → a single buffered `PreviewDigest` object, with an unusually candid
   doc-comment: *"Note that this does not emit events incrementally so that it can guarantee anything
   emitted to stdout is well-formed. This means that, if used interactively, the experience will
   lead to potentially very long pauses. If run in CI, it is up to the end user to ensure that
   output is periodically printed to prevent tools from thinking preview has hung."*
3. **`PULUMI_ENABLE_STREAMING_JSON_PREVIEW`** makes (2) behave like (1). Documented at
   <https://www.pulumi.com/docs/iac/cli/environment-variables/> with no experimental marker and no
   statement about whether it will become the default. Notably, the `pulumi up --json` docs never
   state that it is a *stream* at all — that is only implied by the env-var entry.
4. **`--event-log <file>`** — `startEventLogger`, always the streaming JSONL form, written
   **upstream of every display renderer**, so you get the machine stream *and* the human view
   simultaneously. Also accepts `tcp://host:port`, in which case events go over gRPC
   `pulumirpc.EventsClient.StreamEvents`. **Undocumented** — absent from every CLI reference page.

Plus `--output json` (`SummaryJSON`), handled by `tapSummaryJSON`: drains the stream and emits one
line on `SummaryEvent`, with the comment *"stdout must contain only the summary JSON line. Suppress
all human-readable output (permalinks, progress, diffs)."*

`stampEvents` assigns sequence and timestamp **once**, upstream of both the log-file tee and the
stdout encoder, explicitly *"so we get consistent timestamps written in both `startEventLogger` and
`ShowJSONEvents`."*

### Stability: no guarantee, and no disclaimer either

The only statement anywhere is this comment in `sdk/go/common/apitype/events.go`:

```go
// The "engine events" defined here are a fork of the types and enums defined in the engine
// package. The duplication is intentional to insulate the Pulumi service from various kinds of
// breaking changes.
//
// The types aren't versioned in the same manner as Resource, Deployment, and Checkpoint (see
// apitype/migrate). So care must be taken if these are ever returned from the service to the CLI.
```

Three consequences:

1. **Explicitly not versioned.** The fork protects the *service* from engine churn — not consumers
   from apitype churn.
2. The only stability affordance is per-field, ad hoc and reactive. `SummaryEvent.PolicyPacks`
   carries: *"Note: When this field was initially added, we forgot to add the JSON tag and are now
   locked into to using PascalCase for this field to maintain backwards compatibility."* That is
   compat by accident, not by policy.
3. **`apitype/migrate` — the package that comment points at as the contrast case — no longer exists
   on `master`.** It was present at v3.100.0. And there is no `events.json` schema alongside the
   `deployments.json` / `resources.json` / `property-values.json` schemas that *do* ship in
   `apitype/`. Events have neither a migration path nor a published schema.

Docs side is total silence — nothing about stability, versioning, experimental status or
"subject to change" on the
[Automation API pages](https://www.pulumi.com/docs/iac/using-pulumi/automation-api/) (which do not
mention engine events at all) or the generated `EngineEvent` reference. Consuming `--json` is an
**unversioned, undocumented contract**: not "subject to change", but *nothing claimed in either
direction*.

### Non-TTY: two independent checks, then strictly append-only

**Layer 1**, `sdk/go/common/util/cmdutil/console.go`, feeding `Options.IsInteractive`:

```go
func Interactive() bool { return !DisableInteractive && InteractiveTerminal() && !ciutil.IsCI() }

func InteractiveTerminal() bool {
	if v := strings.ToLower(os.Getenv("TERM")); v == "dumb" { return false }
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
```

Note it checks **stdin and stdout** (not stderr), so `pulumi up < /dev/null` on a real TTY is already
non-interactive. `DisableInteractive` is a package global bound to the root `--non-interactive` flag;
there is no `PULUMI_DISABLE_INTERACTIVE` env var.

**Layer 2**, a stricter check inside the display itself (`progress.go`):

```go
if isInteractive && term == nil {
	raw := runtime.GOOS != "windows"
	t, err := terminal.Open(stdin, stdout, raw)
	if err != nil {
		fmt.Fprintln(stderr, "Failed to open terminal; treating display as non-interactive (%w)", err)
		isInteractive = false
	} else { term = t }
}
```

`terminal.Open` demands stdout implement `Fd() uintptr`, that `term.GetSize` succeed, and that
width/height be non-zero (`"unusable dimensions (%v x %v)"`); it reads `TERM`, defaulting to
`vt102`. So even an `IsInteractive: true` run degrades gracefully, with a warning on stderr.

The branch bites at every `messageRenderer` method:

```go
func (r *messageRenderer) tick() {
	if r.isInteractive { r.render(false) } else { r.nonInteractiveSpinner.Tick() }
}
func (r *messageRenderer) rowUpdated(row Row) {
	if r.isInteractive { r.render(false) }
	else if !row.HideRowIfUnnecessary() { r.renderRow("", colorizedColumns, nil) }
}
func (r *messageRenderer) render(done bool) { if !r.isInteractive || r.display.headerRow == nil { return } ... }
```

Cursor movement lives in `ShowProgressOutput`, and is gated by `term` being nil:

```go
// ShowProgressOutput displays a progress stream from `in` to `out`, `isInteractive` describes if
// `out` is a terminal. If this is the case, it will print `\n` at the end of each line and move the
// cursor while displaying.
```

`ProgressDisplay.isTerminal` also gates **content**, not just layout: `getStepOp` returns the raw op
non-interactively rather than collapsing a replace-series into a single `replace`, and the prelude is
printed directly rather than routed through a diagnostic row.

### Parallelism and long-running work

**Interactive: a 60 fps repaint over a re-derived tree.** `treeRenderer` runs two independent clocks:

- **Repaint** — `ticker: time.NewTicker(16 * time.Millisecond)` driving `frame(false, false)`, which
  early-returns on `if !done && !r.dirty { return }`. Every renderer callback does nothing but
  `markDirty()`. Event volume is therefore decoupled from paint rate: 500 in-flight resources still
  cost one repaint per frame.
- **Content** — `ProgressDisplay` runs its own `time.NewTicker(1 * time.Second)`, selected against
  the event channel in `processEvents`. This advances the elapsed-second counters and the
  `"", ".", "..", "..."` suffix animation.

`frame` rewinds by line count (`rewind int // The number of lines we need to rewind to redraw the
entire screen`) and repaints a fixed layout — treetable header, contents, footer, system-messages
header, contents, status line. The table is **re-derived from scratch every frame**:
`generateTreeNodes()` → `filterOutUnnecessaryNodesAndSetDisplayTimes()` → `sortNodes()` →
`addIndentations()` → `convertNodesToRows()`. Columns are `["", Type, Name, Status|Plan, Info]`, or
`["", URN, Status|Plan, Info]` under `ShowURNs`; the header cell is `"Plan"` during preview and
`"Status"` during update. The tree scrolls (`treeTableOffset`) and takes keys via `pollInput()` —
Ctrl+C interrupts, Ctrl+O opens the permalink.

Elapsed time lives in `opStopwatch` (`start`/`end` maps keyed by URN, guarded by `stopwatchMutex`),
populated on `ResourcePreEvent`/`ResourceOutputsEvent`. In-flight is
`fmt.Sprintf("%s (%ds)", opText, int(secondsElapsed))`; completed switches to sub-second precision
below 1s (`"%.2fs"`). Suppressed when `op == deploy.OpSame`, or under `opts.DeterministicOutput` /
`opts.SuppressTimings` — which is how the golden tests get stable output.

**Non-interactive: a dot per second, and nothing else until the resource finishes.** The row is
printed once at `ResourcePreEvent` and once at `ResourceOutputsEvent`; in between there is only the
spinner heartbeat. `NewSpinnerAndTicker` returns a `dotSpinner` when `!Interactive()`, which prints a
yellow prefix once and then a `.` per tick, with `Reset()` emitting the closing newline.

Two details worth knowing: the cadence is **1s, not 20s** — `NewSpinnerAndTicker`'s own
non-interactive ticker is `time.NewTicker(time.Second * 20)`, but `newNonInteractiveRenderer`
immediately `.Stop()`s it and lets `ProgressDisplay`'s 1-second ticker drive `tick()`. And
`processTick` carries a stale comment (*"print a hearbeat message every 10 seconds"*) that matches
neither the interval nor the behaviour. `--suppress-progress` swaps in `noopSpinner`.

`processEndSteps` is the only place a still-running resource's final state reaches non-interactive
output — it collects rows where `!v.IsDone()`, optionally sorts them under `DeterministicOutput`, and
flushes them.

Plugin downloads (`ProgressEvent`) get a real progress bar interactively (`progress_bar.go`,
`renderUnicodeProgressBar` / `renderASCIIProgressBar` → `[--->_____]`) and collapse non-interactively
to exactly two lines: `"<msg>: starting"` and `"<msg>: done"`.

### `NO_COLOR` and CI

`--color` is one root persistent flag —
`StringVar(&color, "color", "auto", "Colorize output. Choices are: always, never, raw, auto")` —
parsed via `cmdutil.SetGlobalColorization`. Note **`auto` is represented as `nil`**: there is no
`colors.Auto`, only `Always`, `Never`, `Raw`.

```go
func GetGlobalColorization() colors.Colorization {
	if globalColorization != nil { return *globalColorization }
	if _, ok := os.LookupEnv("NO_COLOR"); ok { return colors.Never }
	if !InteractiveTerminal() { return colors.Never }
	return colors.Always
}
```

So: `NO_COLOR` **is** honoured, presence-based (`NO_COLOR=` empty still disables), but an explicit
`--color=always` beats it via the early return. No `PULUMI_COLOR`, `FORCE_COLOR` or `CLICOLOR`
exists. Windows adds a hard override — `colors/windows.go` sets
`disableColorization = !hasVTSupport()` in `init()`, short-circuiting even `--color=always`.

Colour reaches machine output too: `logJSONEvent` strips colour directives from event payloads when
`opts.Color == colors.Never`, and the docs confirm it — *"When used with Automation API, this
environment variable will strip color directives from the event logs."*

CI detection lives in `sdk/go/common/util/ciutil` and keys ~24 systems off specific env vars
(`PULUMI_CI_SYSTEM` first, then `APPVEYOR`, `CODEBUILD_BUILD_ARN`, `TF_BUILD`, `BUILDKITE`,
`CIRCLECI`, `GITHUB_ACTIONS`, `GITLAB_CI`, `JENKINS_URL`, `TRAVIS`, …). **A bare `CI=true` is not a
detector.** `PULUMI_DISABLE_CI_DETECTION` is the escape hatch, with the comment *"Provide a way to
disable CI/CD detection, as it can interfere with the ability to test."*

There are exactly three non-test importers of `ciutil`: `cmdutil/console.go` (`Interactive()`),
`securestore/attended.go` (gating password dialogs), and `metadata.go` (populating
`UpdateMetadata`). Which yields the decisive answer:

- CI **does not** change the display type — `Type` is a pure function of `--diff`.
- CI **does** change interactivity, transitively, because `Interactive()` ANDs in `!ciutil.IsCI()`.
  GitHub Actions *with a TTY attached* still yields `IsInteractive: false`, and `up` without `--yes`
  fails outright with `backenderr.NoConfirmationInNonInteractiveError`.
- CI **does not** change colour — `GetGlobalColorization()` never calls `ciutil`, it uses
  `InteractiveTerminal()`. **So a CI job with a PTY gets non-interactive layout but full colour.**
  The two decisions deliberately use different predicates, and this is nowhere documented.

Doc divergence worth flagging: the
[CI/CD guide](https://www.pulumi.com/docs/iac/guides/continuous-delivery/) describes CI detection
purely as metadata capture — *"it reads the environment variables that system injects into its build
agents and captures metadata about the build"* — and **no Pulumi page states that CI detection
disables the interactive renderer**. It does.

### Caveats

- `PreviewDigest`'s schema has no reference page and no JSON schema in `apitype/`.
- `--event-log` is real in code but absent from all published CLI reference pages, so its
  hidden/experimental status is undocumented.
- `colors.disableColorization` is written only by `windows.go`; that it stays false elsewhere is
  inferred from the absence of another writer.

---

## Synthesis

### On the key question, the three tools sit at three different points

- **Pulumi — one stream, many renderers, provably.** The human view is a pure function of the typed
  event stream, and `pulumi replay-events` proves it by reconstructing the display from serialized
  JSON through the same entry point. This is the design to aim at.
- **CDK — split down the middle.** `deploy` does it right: a typed activity stream with the human
  printers registered as *listeners* (`preventDefault: true`), so a machine consumer just registers a
  different listener. `diff` does it wrong: the producer renders the string, staples it onto the
  payload as `formattedDiff`, and the "structural" half is a non-serializable class graph nobody
  consumes.
- **Terraform — two renderers, one plan.** The human view and `-json` are independently written,
  down to two separate tally loops over the same `plans.Changes` and two independent `Hook`
  implementations with their own goroutines on the same 10s timer. The sharing that *does* exist is
  one level down: `jsonplan` + `jsonformat` is a genuine structured intermediate, but it is shared
  between the human plan diff and `terraform show -json`, not between human and the streaming
  `-json`. The practical cost is stark — **you cannot reconstruct the human plan diff from the
  `-json` stream**, because `planned_change` carries no attribute-level before/after.

### The stability guarantee is the part worth copying verbatim

CDK's message registry has the best-written version, and it is short enough to steal:

> Depend only on messages and requests with a `code`. Treat all other messages as informational only.
> Only the `code` and `data` properties of a message are in scope for backwards compatibility. […]
> the following changes are explicitly not considered breaking: a change to the message text or
> level, a change to the default response of a request, a change to the order messages and requests
> are emitted in, the addition of new messages and requests, and the removal of messages without a
> code.

Terraform's is a versioned handshake with forward-compat rules spelled out for the consumer (ignore
unknown properties on a minor bump; reject an unsupported major), plus the general instruction to
fall back to `@message` for unknown types. Its weakness is drift: the prose says `ui: "1.0"` while
the constant says `"1.3"`, and the emitted type list is a strict superset of the documented one.

Pulumi has **nothing** — not a guarantee, not a disclaimer. The comment pointing at `apitype/migrate`
as the contrast case now points at a package that no longer exists.

Three rules fall out for a stream Ocel wants integrators on:

1. Version the stream and say what a minor and a major bump mean *for the consumer*.
2. Name a match key (a code) and say explicitly that message text, level and emission order are
   **not** part of the contract.
3. Enumerate the types in a generated, checked-in registry, or the documented list drifts behind the
   emitted one the way Terraform's has.

### Non-TTY: line-per-event is the universal fallback, but the checks differ

All three converge on strictly append-only, one-line-per-event, no cursor control when there is no
terminal. Where they differ is how many questions they ask:

- Terraform asks **none** for the human view — it never redraws in the first place, so there is
  nothing to turn off. It detects width only (falling back to 78 columns), and it leaves colour on
  when piped, which is why `-no-color` is mandatory in CI.
- Pulumi asks **twice** — `cmdutil.Interactive()` (stdin *and* stdout are TTYs, `TERM != dumb`, not
  CI, not `--non-interactive`), then `terminal.Open` inside the display, which can still fail and
  degrade with a warning on stderr.
- CDK asks a **five-step cascade** — explicit choice, then verbose logging, then platform (Windows),
  then `isTTY && !isCI`, then the default. Each step is a real reason, and the CircleCI comment
  (*"output still reports as a TTY so we also need an individual check for whether we're running on
  CI"*) is why the TTY check alone is insufficient.

Parallel work is rendered two ways and only two: **interleaved self-identifying lines** (Terraform,
always; Pulumi and CDK non-interactively) or a **redrawn region re-derived per frame** (Pulumi's
`treeRenderer`, CDK's `CurrentActivityPrinter`). Nobody attempts per-resource cursor regions. The
enabling detail on the line-based side is that every line carries the resource address (Terraform)
or the deployment UUID (CDK's `StackActivity.deployment`), so a consumer can regroup what the
terminal interleaved.

Long-running resources get a periodic heartbeat in every tool, at wildly different cadences:
Terraform 10s with elapsed time and resource address on both the human and JSON sides; CDK 30s of
silence before re-listing what is in progress; Pulumi one bare `.` per second carrying no
information at all.

### `NO_COLOR` and CI: nobody gets this fully right

- **Terraform** does not read `NO_COLOR` at all, and does no TTY-based colour detection —
  `terraform plan | cat` emits raw ANSI.
- **CDK** does not read it in product code either; it works only because `chalk` reads it, and
  `cdk --color` sets `FORCE_COLOR=3`, which *overrides* a user's `NO_COLOR`.
- **Pulumi** reads it in exactly one function, presence-based, and lets `--color=always` win. But its
  colour decision uses `InteractiveTerminal()` while its layout decision uses `Interactive()` (which
  additionally ANDs in `!IsCI()`), so **a CI job with a PTY gets non-interactive layout and full
  colour**.

The generalisable lesson is that "is this a terminal", "is this CI", "should this be interactive" and
"should this be coloured" are four different questions, and every tool here has at least one bug or
surprise from collapsing two of them. CDK goes furthest by splitting CI into three: the force flag,
the env sniff, and *"is stderr safe on this specific CI system"* (`ci-systems.ts`, with a
`canBeConfiguredToFailOnStdErr` flag per vendor).

Two more transferable ideas:

- **Stream policy should be level-based, not mode-based.** CDK: `result` → stdout always, `error` →
  stderr always, everything else moves to stdout under CI. That keeps `cdk synth > file.yaml` correct
  in every mode.
- **Agent detection is a real third audience.** CDK auto-selects `--progress errors-only` when it
  detects an agent driving the CLI, on the reasoning that *"progress updates are wasted tokens for AI
  agents"*, and draws a careful line at variables *set by the agent itself* rather than ones that
  merely indicate an agent-capable IDE.

### Fit against what Ocel has today

`cli/internal/deployui/renderer.go` and `cli/internal/changeplan/changeplan.go` currently sit closest
to **Terraform's** position, and in one respect behind it.

`Renderer` is a single type carrying a `Format` (`FormatHuman` / `FormatJSON`, selected by
`--log-format`, `root.go`), and **every method branches on it at the top**:

```go
func (r *Renderer) Progress(...) {
	if r.format == FormatJSON {
		fields := map[string]any{"phase": phaseTag(phase), "message": message}
		...
		r.emitJSON("progress", fields)
		return
	}
	...
}
```

Thirteen methods repeat that shape. The consequences track the prior art exactly:

- **The two views have diverged in content, not just presentation.** `StageEnd` returns early under
  JSON and emits nothing at all. `StagePlan` emits `{final, count}` — the count, not the stages, so
  the tree structure the human view renders is unreachable from JSON. `Building()` deliberately nils
  the stage id under JSON. `BuildOK` has no JSON path. This is Terraform's `planned_change`-carries-
  no-attribute-diff problem in miniature.
- **The JSON shape is untyped and constructed inline.** `emitJSONLocked` takes a `map[string]any`
  and marshals `{"type": kind, ...fields}`. There is no schema, no version field, no enumerated type
  list — which is Pulumi's position, minus Pulumi's `apitype` fork. Notably the *inputs* are already
  typed protos (`progressv1.StagePlanEvent`, `progressv1.Phase`, `contractv1.ChangePlan`), so the
  typed event stream largely exists already; it is discarded at the renderer boundary.
- **`changeplan.Printer` has no machine path at all.** It is human-only — `Print`, `Render`,
  `GroupLine`, `Tally` all write strings — which is CDK's `cdk diff` position: a rich typed input
  (`contractv1.ChangePlan`, with `Change_Action`, groups, reasons, `Slow`) rendered by exactly one
  renderer, with no way for a consumer to get the plan as data. The tally, the sigils
  (`+ ~ ± –`) and the roll-up logic in `rolledUp`/`partition` are presentation decisions computed
  inside the printer, so a second consumer would have to re-derive them.

On the mechanics Ocel is already in reasonable shape and in places ahead of the prior art:

- `live` is `format == FormatHuman && !verbose && IsTerminal(w)` — the verbose-forces-line-mode rule
  matches CDK's step 2 (redrawing fights interleaved logs), and it is a rule Terraform doesn't need
  and Pulumi doesn't have.
- The tick loop, `eraseLiveLocked`/`drawLiveLocked`, and re-deriving `displayRows()` per frame is
  structurally Pulumi's `treeRenderer`, including bounding the drawn region
  (`effectiveMaxRowsLocked`, `termHeight - 3`) and the `… and %d more` overflow line.
- `Write` erasing and redrawing around foreign output is the thing that makes a redrawn region
  survive a subprocess writing to the same stream — neither Terraform nor CDK needs this because
  neither interleaves.
- Colour goes through `fatih/color`, and `changeplan.NewPrinter` reads `color.NoColor` (which honours
  `NO_COLOR` and TTY detection), so Ocel already does what Terraform doesn't and CDK only does by
  proxy. But `deployui.NewRenderer` sets `color: IsTerminal(w)` and **ignores `color.NoColor`** — so
  `NO_COLOR=1` is honoured by the change plan and ignored by the deploy renderer. That is exactly the
  Pulumi asymmetry (two colour decisions, two predicates) reproduced locally.
- There is no CI detection anywhere, and no equivalent of `TF_IN_AUTOMATION` /
  `PULUMI_DISABLE_CI_DETECTION` / `--ci`. Today TTY-ness is the only axis.

The shape the prior art points at, if the divergence is to be closed: make the typed progress protos
the single stream, render both views as subscribers to it (CDK's `deploy`, Pulumi's `ShowEvents`),
give `changeplan` a data emitter rather than only a printer, and — before anyone integrates — write
down the CDK-style "code and data only" contract so the human strings stay free to change.
