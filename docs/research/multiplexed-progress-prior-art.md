# Multiplexed parallel progress: prior art, and a BuildKit → `progressv1` mapping

Research for [#617](https://github.com/ocelhq/ocel/issues/617), part of the compute-abstraction
map [#614](https://github.com/ocelhq/ocel/issues/614). Feeds
[#621](https://github.com/ocelhq/ocel/issues/621) (build phases and docker build logs in the stage
tree) and [#622](https://github.com/ocelhq/ocel/issues/622) (multi-app realtime view and failure
isolation).

BuildKit is the load-bearing case because [#577](https://github.com/ocelhq/ocel/issues/577) locked
Railpack plan generation and the BuildKit frontend as in-process in the Go CLI — no shell-out to
`docker build`, no `buildx` binary. The CLI therefore owns the `chan *client.SolveStatus` directly
and inherits none of buildx's rendering. Whatever a build looks like on screen is something ocel
builds out of its own event vocabulary.

## Contents

1. [BuildKit: the wire model](#1-buildkit-the-wire-model)
2. [BuildKit: `progressui` display modes](#2-buildkit-progressui-display-modes)
3. [buildx bake and docker compose](#3-buildx-bake-and-docker-compose)
4. [Bazel: scrolling log plus pinned status block](#4-bazel-scrolling-log-plus-pinned-status-block)
5. [turborepo: stream vs grouped](#5-turborepo-stream-vs-grouped)
6. [Recurring conventions](#6-recurring-conventions)
7. [ocel today](#7-ocel-today)
8. [The mapping: `SolveStatus` → `OperationEvent`](#8-the-mapping-solvestatus--operationevent)
9. [What the mapping breaks](#9-what-the-mapping-breaks)
10. [Open questions handed to #621 and #622](#10-open-questions-handed-to-621-and-622)

---

## 1. BuildKit: the wire model

Source: `moby/buildkit`, [`client/graph.go`](https://github.com/moby/buildkit/blob/master/client/graph.go).
The conversion helpers to and from the `controlapi` proto types live in
[`client/status.go`](https://github.com/moby/buildkit/blob/master/client/status.go), not in
`graph.go`.

```go
type SolveStatus struct {
	Vertexes []*Vertex        `json:"vertexes,omitempty"`
	Statuses []*VertexStatus  `json:"statuses,omitempty"`
	Logs     []*VertexLog     `json:"logs,omitempty"`
	Warnings []*VertexWarning `json:"warnings,omitempty"`
}

type Vertex struct {
	Digest        digest.Digest     `json:"digest,omitempty"`
	Inputs        []digest.Digest   `json:"inputs,omitempty"`
	Name          string            `json:"name,omitempty"`
	Started       *time.Time        `json:"started,omitempty"`
	Completed     *time.Time        `json:"completed,omitempty"`
	Cached        bool              `json:"cached,omitempty"`
	Error         string            `json:"error,omitempty"`
	ProgressGroup *pb.ProgressGroup `json:"progressGroup,omitempty"`
}

type VertexStatus struct {
	ID        string        `json:"id"`
	Vertex    digest.Digest `json:"vertex,omitempty"`
	Name      string        `json:"name,omitempty"`
	Total     int64         `json:"total,omitempty"`
	Current   int64         `json:"current"`
	Timestamp time.Time     `json:"timestamp"`
	Started   *time.Time    `json:"started,omitempty"`
	Completed *time.Time    `json:"completed,omitempty"`
}

type VertexLog struct {
	Vertex    digest.Digest `json:"vertex,omitempty"`
	Stream    int           `json:"stream,omitempty"`
	Data      []byte        `json:"data"`
	Timestamp time.Time     `json:"timestamp"`
}

type VertexWarning struct {
	Vertex     digest.Digest  `json:"vertex,omitempty"`
	Level      int            `json:"level,omitempty"`
	Short      []byte         `json:"short,omitempty"`
	Detail     [][]byte       `json:"detail,omitempty"`
	URL        string         `json:"url,omitempty"`
	SourceInfo *pb.SourceInfo `json:"sourceInfo,omitempty"`
	Range      []*pb.Range    `json:"range,omitempty"`
}
```

Four properties drive everything downstream.

**Identity is a content digest.** A vertex is keyed by `Digest`, stable across runs for the same
inputs, and carrying no ordinal position. Nothing announces the vertex set in advance; vertices
appear as the solver reaches them. There is no plan event.

**The graph is a DAG.** `Inputs` is a slice — a merge or a multi-stage `COPY --from` genuinely has
two parents. `progressui` never reconstructs it: it keeps a flat `t.vertexes` ordered by start
time and derives indentation from `v.indent`, a string, never from `Inputs`. BuildKit's own UI
concluded the DAG was not worth drawing.

**One vertex, three kinds of sub-event.** `VertexStatus` is a named counter *inside* a vertex,
keyed by its own `ID`, so one vertex can carry many live counters (one per layer being pulled).
`VertexLog` is a byte chunk with a `Stream` discriminator (1 stdout, 2 stderr) that is not
line-aligned — a chunk can be half a line. `VertexWarning` carries a short form, a detail body, a
URL and source ranges; it is a lint result, not a log line.

**`Completed` does not mean success.** Finished is `Completed != nil`; succeeded is `Error == ""`;
did-any-work is `!Cached`. Cached vertices commonly arrive already complete in the same batch that
first announces them.

### ProgressGroup: the collapse mechanism

[`solver/pb/ops.proto`](https://github.com/moby/buildkit/blob/master/solver/pb/ops.proto), with
the LLB constructor in `client/llb/state.go`:

```proto
message ProgressGroup {
	string id = 1;
	string name = 2;
	bool weak = 3;
}
```

In `util/progress/progressui/display.go`, `trace.update` reacts to a non-nil
`Vertex.ProgressGroup` by minting or reusing a synthetic `vertexGroup` keyed on
`ProgressGroup.Id`, remapping `t.byDigest[v.Digest]` onto the group, and filing the real vertex
under `group.subVtxs`. `vertexGroup.refresh()` recomputes a synthetic `client.Vertex` per tick:
`Cached` only if every member is cached, `Error` the first non-empty member error, member
intervals unioned into `vg.mergedIntervals` so the group reports one elapsed span.

The `weak` bit gates visibility. A group starts `hidden: true` and reveals itself only once a
**non-weak** member has started, so cache probes and metadata steps never surface a group on their
own. Both renderers then skip members outright — `if v.ProgressGroup != nil || v.hidden { continue }`
— and only the merged row is ever drawn.

This is the most transferable idea in the survey: the producer, not the renderer, declares which
fan-out is one conceptual row, and it declares it with a group id plus a flag meaning "this member
alone does not count".

---

## 2. BuildKit: `progressui` display modes

[`util/progress/progressui/display.go`](https://github.com/moby/buildkit/blob/master/util/progress/progressui/display.go),
plus `printer.go`, `init.go`, `colors.go`.

```go
type DisplayMode string
const (
	DefaultMode DisplayMode = ""
	AutoMode    DisplayMode = "auto"
	QuietMode   DisplayMode = "quiet"
	TtyMode     DisplayMode = "tty"
	PlainMode   DisplayMode = "plain"
	RawJSONMode DisplayMode = "rawjson"
)
```

`NewDisplay` resolves `auto`/`tty`/default by trying `consoleFromWriter`. On failure `auto` and
default degrade silently to plain; an explicit `tty` errors with `"failed to get console"`.
`quiet` returns a discard display.

### tty

Header, verbatim from `ttyDisplay.print`:

```go
out := fmt.Sprintf("[+] %s %.1fs (%d/%d) %s",
	disp.phase, time.Since(d.startTime).Seconds(), d.countCompleted, d.countTotal, statusStr)
```

`disp.phase` defaults to `"Building"`. `statusStr` is `"FINISHED"` only on the final draw, when
`countCompleted == countTotal && all`. Job rows are `" => "` plus name plus a right-aligned
`" %3.1fs"` timer, name truncated to fit. `CACHED `, `ERROR ` and `CANCELED ` are prepended to the
name in `trace.displayInfo()`, not formatted by the printer — the status is already part of the
string by the time a row renders it.

Live command output rides inside the row block one indent deeper, as `" => => # %s\n"` in
`aec.Faint`, fed from a per-vertex `vt100.VT100` virtual terminal (`v.term`) sized to
`(termHeight, width-termPad)`.

Row budget (`setupTerminals`, `wrapHeight`):

- `termHeightMin = 6`, and `termHeight` defaults to it (`BUILDKIT_TTY_LOG_LINES` overrides);
  `termPad = 10` reserves the timer column.
- `numFree := height - 2 - numInUse`, `termLimit := termHeight + 3`. Promotion to "show your
  embedded terminal" is ranked by `termBytes + termCount*50`, so the vertices producing the most
  output and updating most often win the log real estate.
- `wrapHeight(jobs, height-2-numToHide)` truncates the row list but **re-inserts any in-progress
  job that would have been cut**. Running steps never scroll off; a completed row is dropped
  instead.

Redraw is throttled twice over: a `time.Ticker` at `tickerTimeout = 150ms` (`TTY_DISPLAY_RATE`)
drives `refresh()`, and a `rate.Limiter` at `displayTimeout = 100ms` gates update-driven redraws.

### plain

`printer.go`, type `textMux`. Constants: `antiFlicker = 5s`, `maxDelay = 10s`, `minTimeDelta = 5s`,
`minProgressDelta = 0.05`, `logsBufferSize = 10`.

Vertex numbering is lazy — `v.index = p.nextIndex++` on first flush — so `#N` is first-printed
order, not creation order. A vertex introduces itself with
`fmt.Fprintf(p.w, "#%d %s\n", v.index, limitString(v.Name, 72))`, its log lines print as `#%d %s`
where the string already carries an elapsed prefix baked in upstream (giving the familiar
`#7 0.523 npm WARN …`), and it terminates with one of `#%d DONE %.1fs`, `#%d CACHED`,
`#%d CANCELED`, `#%d ERROR: %s`.

The scheduler is the interesting part. `textMux.print` flushes completed vertices first, sorted by
completion time, then keeps the current vertex while it is still producing, else picks the
highest-`speed` (`count / blockTime`) unprinted candidate. Any vertex blocked longer than
`maxDelay` is force-surfaced; `antiFlicker` stops one chatty vertex from monopolising the stream.
When the active vertex changes mid-line, the outgoing one prints a bare `#N ...` continuation
marker before the newcomer's header. That marker plus the stable `#N` is the entire disambiguation
strategy: a reader grepping `#7` reconstructs one vertex's log out of a file interleaving six.

Per-vertex log retention is a 10-entry `ring.Ring` (`logsBufferSize`), used only by
`trace.printErrorLogs` to re-print a failed vertex's tail at the end. `v.logs`, `v.logsOffset` and
`v.logsPartial` track the unflushed tail of a partial line and are a separate mechanism.

### rawjson

```go
func newRawJSONDisplay(w io.Writer) Display {
	enc := json.NewEncoder(w)
	return Display{disp: &rawJSONDisplay{enc: enc, w: w}}
}
func (d *rawJSONDisplay) update(ss *client.SolveStatus) { _ = d.enc.Encode(ss) }
```

One JSON-encoded `*client.SolveStatus` per update, per line. No aggregation, no downsampling, no
schema of its own. The machine-readable mode *is* the raw stream, and every human mode is a lossy
view over it.

---

## 3. buildx bake and docker compose

**buildx has no renderer of its own.** `commands/bake.go` does
`progressMode := progressui.DisplayMode(cFlags.progress)` and hands it to `progress.NewPrinter`,
which (`util/progress/printer.go`) wraps `progressui.NewDisplay` in a goroutine reading a
`chan *client.SolveStatus`; `BUILDKIT_PROGRESS` overrides when the mode is `auto`.

Multi-target disambiguation is a stream rewrite, `util/progress/multiwriter.go`:

```go
func addPrefix(pfx, name string) string {
	if strings.HasPrefix(name, "[") {
		return "[" + pfx + " " + name[1:]
	}
	return "[" + pfx + "] " + name
}
```

`build/build.go` does `pw := progress.WithPrefix(w, k, multiTarget)`, `k` being the target name and
`multiTarget` true whenever bake builds more than one. Every `Vertex.Name` and every
`ProgressGroup.Name` picks up a `[target]` prefix in transit, and all targets flow into one
`progressui.Display`. Attribution is baked into the name; the renderer stays single-stream and
unaware; grouping still collapses, now namespaced per target.

`--progress` values (docs.docker.com/reference/cli/docker/buildx/build/): `auto` (default: tty if a
TTY, else plain), `tty`, `plain`, `quiet`, `rawjson`.

**compose splits the two problems.** For builds it imports buildkit directly —
`pkg/compose/build_bake.go` calls `progressui.NewDisplay(makeConsole(out), displayMode)` then
`display.UpdateFrom(ctx, ch)`, the same path as buildx. For container logs it has its own
formatter, `cmd/formatter/logs.go`:

```go
p.prefix = p.colors(fmt.Sprintf("%-"+strconv.Itoa(width)+"s | ", p.name))
```

`width` is `computeWidth()`, the longest registered service name plus one, so every prefix pads to
a common column and produces the `web-1     | line` shape. `cmd/formatter/colors.go` cycles a
fixed 10-entry `rainbow` (cyan, yellow, green, magenta, blue, then bold variants) round-robin per
registered service. `--no-log-prefix` drops the prefix; `--attach` / `--no-attach` /
`--attach-dependencies` select which services stream at all (`--attach` and
`--attach-dependencies` are mutually exclusive).

The split is the lesson. A build is a DAG and gets a redrawing status block. N long-lived
processes are a flat set and get padded, coloured line prefixes. One tool, two shapes, because the
underlying concurrency has two shapes.

---

## 4. Bazel: scrolling log plus pinned status block

Sources:
[`UiEventHandler.java`](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/runtime/UiEventHandler.java),
[`UiStateTracker.java`](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/runtime/UiStateTracker.java),
[`UiOptions.java`](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/runtime/UiOptions.java).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--curses` | `auto` | "Use terminal cursor controls to minimize scrolling output." |
| `--color` | `auto` | Colorize output. |
| `--show_progress` | `true` | Display progress messages during a build. |
| `--show_progress_rate_limit` | `0.2` s | Minimum seconds between progress messages. |
| `--ui_actions_shown` | **`8`** | "Number of concurrent actions shown in the detailed progress bar; each action is shown on a separate line… all numbers less than 1 are mapped to 1." |
| `--experimental_ui_max_stdouterr_bytes` | `1048576` | Cap on the stdout/stderr dumped per action; `-1` disables. |
| `--progress_in_terminal_title` | `false` | Mirror progress into the terminal title. |
| `--attempt_to_print_relative_paths` | `false` | Shorten locations relative to the workspace. |
| `--ui_event_filters` | empty | Which event kinds reach the UI at all, `+`/`-` deltas or full override. |

**Erasing the pinned block** is a per-line ANSI loop, `clearProgressBar()`:

```java
private void clearProgressBar() throws IOException {
  if (!cursorControl) { return; }
  for (int i = 0; i < numLinesProgressBar; i++) {
    terminal.cr();
    terminal.cursorUp(1);
    terminal.clearLine();
  }
  numLinesProgressBar = 0;
}
```

`addProgressBar()` redraws through a `LineCountingAnsiTerminalWriter` wrapped in a
`LineWrappingAnsiTerminalWriter` (only when `cursorControl`), re-measuring `numLinesProgressBar`
each draw — so wrapped long lines are counted correctly on the next erase.

**Output is flushed above the block, buffered to line boundaries** (`writeToStream`):

```java
int eolIndex = Bytes.lastIndexOf(message, (byte) '\n');
ByteArrayOutputStream outLineBuffer = eventKind == EventKind.STDOUT ? stdoutLineBuffer : stderrLineBuffer;
if (eolIndex < 0) { outLineBuffer.write(message); return false; }
clearProgressBar();
terminal.flush();
outLineBuffer.writeTo(stream);
outLineBuffer.reset();
stream.write(message, 0, eolIndex + 1);
stream.flush();
outLineBuffer.write(message, eolIndex + 1, message.length - eolIndex - 1);
```

Partial lines wait in the buffer until a newline arrives; only then is the bar cleared, the line
written, and the bar redrawn.

**Action output never interleaves** because it is not streamed at all. `handleLocked()` receives
each action's stdout and stderr as `byte[]` already read from disk ("to avoid blocking within the
critical section") and dumps them as one atomic block when the completion event fires. The gate is
the event, not success or failure — succeeding and failing actions alike are captured to files
during execution and printed whole. `getContentIfSmallEnough()` enforces the cap:

```java
if (size <= maxStdoutErrBytes) { return getContent.get(); }
return String.format("%s (%s) %d exceeds maximum size of "
    + "--experimental_ui_max_stdouterr_bytes=%d bytes; skipping\n", ...);
```

**Non-terminal degradation** is explicit rather than accidental. With `cursorControl` false,
`clearProgressBar()` no-ops, `timeBasedRefresh()` returns false so only event-driven redraws
survive, and the rate limit gains a floor:

```java
private static final long NO_CURSES_MINIMAL_PROGRESS_RATE_LIMIT = 1000L;
this.progressRateLimitMillis = this.cursorControl
    ? Math.round(options.getShowProgressRateLimit() * 1000)
    : Math.max(Math.round(options.getShowProgressRateLimit() * 1000), NO_CURSES_MINIMAL_PROGRESS_RATE_LIMIT);
```

`addProgressBar()` additionally passes `shortVersion = !cursorControl` to
`stateTracker.writeProgressBar(...)`, so the pinned block collapses to a condensed single line in
CI, emitted at most once a second as permanent scrollback.

**Sampling.** `UiStateTracker.sampleSize` defaults to 3 in the field initialiser but
`UiEventHandler`'s constructor immediately calls `setProgressSampleSize(options.getUiActionsShown())`,
so the effective default is 8. `setProgressSampleSize` floors at 1. When more actions are running
than slots, `AND_MORE = " ..."` is appended. Which actions occupy the slots is a priority ordering
— RUNNING phase first, then earliest `nanoStartTime` — so long-running work is favoured over
newly started work, and the sample is recomputed every redraw rather than admitted once.

**The durable record is a separate sink.** `--build_event_json_file` (defaults to empty) writes the
full Build Event Protocol as JSON and implies `--bes_upload_mode=wait_for_upload_complete`. The
per-spawn execution log is now `--execution_log_binary_file` / `--execution_log_json_file` /
`--execution_log_compact_file` (the older `--experimental_execution_log_file` no longer exists in
`ExecutionOptions.java`); all three default to disabled.

---

## 5. turborepo: stream vs grouped

Doc source: <https://turborepo.dev/docs/reference/run> and
<https://turborepo.dev/docs/reference/configuration>.

| Flag | Default | Values |
| --- | --- | --- |
| `--log-order` | `auto` | `stream` (show output as soon as available), `grouped` (group output by task), `auto` (turbo decides) |
| `--output-logs` | `full` | `full`, `hash-only`, `new-only` (cache misses only), `errors-only`, `none` |
| `--log-prefix` | `auto` | `task` (force `<package>:<task>:`), `none`, `auto` |
| `--ui` / `turbo.json` `ui` | `stream` | `tui` (interactive task table plus a per-task output pane), `stream` (non-interactive) |

`auto` resolves to `grouped` under CI and `stream` locally. The rationale is the one Bazel encodes
in its non-curses path: a CI log viewer has no addressable terminal, so concurrently streaming
tasks interleave line by line and become unreadable. Grouped buffers each task's whole output and
emits it as one contiguous block on completion.

Caching and log replay are the same mechanism: "Turborepo always captures the terminal outputs of
your tasks, restoring those logs to your terminal from the first time that the task ran." A cache
hit skips execution, restores declared outputs, and replays the captured output as though the task
had just run. `--output-logs` controls the verbosity of that replay as much as of a live run —
`errors-only` is the setting that says "show me nothing unless it broke".

Per-package log files under `.turbo/turbo-<task>.log` are consistent across turborepo's docs and
issue tracker but I could not pin the current source line constructing that path (the Rust files
have moved and GitHub code search rate-limited); treat the exact path as documentation-confidence,
not source-confirmed. The separate run-level `--log-file` defaults to `.turbo/logs/<timestamp>.json`.

The TUI's `interactive: true` task flag (requires `persistent: true`) forwards stdin to one
selected task — the one surveyed design that treats a live row as something you can talk back to.

---

## 6. Recurring conventions

**Bounded live rows, recomputed per frame.** Nobody shows every running job. BuildKit fits rows to
terminal height and re-inserts in-progress jobs that truncation would have cut. Bazel samples 8 of
the running set by (phase, oldest-first) and appends `" ..."`. Both recompute the visible set on
every redraw, so a job that was crowded out earlier appears the moment a slot frees. Overflow is
always announced, never silent.

**Per-job prefixes when the terminal cannot be addressed.** `#7` (BuildKit plain), `[web]`
(buildx bake), `web-1     |` (compose, padded to a common column and colour-cycled),
`web:build:` (turborepo). The prefix is applied to the *name in the stream*, upstream of the
renderer, in buildx's case — the renderer stays single-stream and attribution rides in the data.

**Buffer-then-replay in CI, interleave on a TTY.** turborepo makes this a documented default flip
(`auto` → `grouped` under CI). Bazel does it structurally: action output is captured to files and
dumped whole at completion, so it never interleaves anywhere, and the non-curses path additionally
raises the progress floor to 1 s and condenses the bar to one line. BuildKit's plain mode keeps
interleaving but makes it recoverable via stable `#N` prefixes plus a fairness scheduler
(`antiFlicker`, `maxDelay`) that stops one vertex hogging the stream.

**Full logs persisted, a summary shown live.** Bazel: `--build_event_json_file` and the execution
logs. turborepo: `.turbo` per-task logs, replayed verbatim on a cache hit. BuildKit: `rawjson` is
the whole `SolveStatus`, and `logsBufferSize = 10` keeps a per-vertex tail purely so a failure can
reprint it. The live view is always the lossy one, and the failure path is where the retained tail
gets spent.

**Byte caps everywhere the retained thing is unbounded.** `--experimental_ui_max_stdouterr_bytes`
= 1 MiB with an explicit "exceeds maximum size; skipping" message rather than a truncation;
`logsBufferSize` = 10 lines; `limitString(v.Name, 72)`.

**Grouping is declared by the producer.** ProgressGroup with its `weak` bit is the only mechanism
surveyed where the thing generating work says "these N vertices are one row, and these members do
not count toward revealing it". Every other tool groups by a name the user already typed (target,
service, package:task).

---

## 7. ocel today

`proto/common/progress/v1/progress.proto` defines the vocabulary; `cli/internal/deployui`
renders it; `pkg/providerkit` produces it.

**Stages.** `StagePlanEvent{stages: [Stage{id, parent_id, title}], final}` declares nodes.
`stagePlan.declare` ignores redeclaration of a known id and buffers children that arrive before
their parent (`maxOrphanParents = 4096`, `maxOrphanChildren = 256`), so out-of-order declaration is
already safe. Ids are `[8]byte`, hex-encoded into map keys by `stageKey`.

**Activation.** `ProgressEvent{stage_id, message, phase, current, total}` runs
`stagePlan.progress`, which marks the node active, sets `n.started = p.now()` — the *local* clock —
and appends to `activeOrder` via `ensureActive`, capped at `maxActiveRows = 20`. Overflow
increments `droppedActive` and the stage is **never admitted later**; `ensureActive` has no retry.

**Termination.** `SpanEvent` is how a stage ends. `Session.ingestSpan` feeds `runtrace` and then
calls `r.StageEnd(spanID[:], …)` — so a span whose `span_id` equals a stage id closes that stage,
with the span's own start/end supplying the committed duration and `SpanStatus` the pass/fail
glyph. `providerkit`'s `reporter.Span` mints a fresh id for ad-hoc spans, so only a span
deliberately emitted with `span_id == stage.ID` terminates a row.

**Rendering.** `Renderer.drawLiveLocked` takes `plan.displayRows()` (a DFS over active roots),
caps at `effectiveMaxRowsLocked() = min(maxActiveRows, termHeight-3)` with a floor of 1, prints
`… and %d more` for the remainder, and repaints by `\033[%dA\033[J` on a `frameRate = 100ms`
ticker. A live row is spinner, two-space-per-depth indent, title, `(i/N)` when `plan.final` and the
node has siblings, elapsed, and then either a 12-cell progress bar (when `total != nil`) or
`— <message>`.

**Logs.** `LogEvent` carries only `message`. `Renderer.Log` returns immediately unless `verbose`,
and `NewRenderer` sets `live = format == FormatHuman && !verbose && IsTerminal(w)`. The live tree
and log lines are therefore mutually exclusive today. `Session.BuildWriter()` sends build output to
the run log file alone unless verbose-and-not-live, in which case it multiwrites to the terminal.

**Persistence.** `Session` opens `<run.Dir()>/<traceID>.log` and mirrors every event into it, then
prints `Details: <path>` on success and `Full log: <path>` on failure. Spans also land in the
OTel-shaped `runtrace`.

**Multi-app today.** `platform/aws/provider/deploy` fans out at `appConcurrency = 4`;
`AppStages` mints one `NewStage(provisioning, app.GetName())` per app, so app work is already
attributed by tree position rather than by prefix.

---

## 8. The mapping: `SolveStatus` → `OperationEvent`

The adapter lives CLI-side, since #577 puts the build in the CLI rather than behind the provider
RPC. It consumes `chan *client.SolveStatus` and emits `progressv1.OperationEvent` into the same
`deployui.Session` the provider stream feeds, so build rows and provisioning rows share one tree.

### Identity

Map `digest.Digest` → `StageID` through a `map[digest.Digest]StageID` holding freshly minted random
ids. Truncating the digest to 8 bytes is tempting (stable across runs, no map) but a collision
silently merges two rows into one, and nothing in the renderer needs cross-run stability. Whichever
way, the zero id must be rejected — `newStageID` already re-rolls on zero and the adapter must
match, because `stageKey(nil) == ""` and `declare` drops empty ids on the floor.

### Shape of the subtree

```
✓ Building
  ⠋ web                          ← per-app build stage, minted by the adapter
    ⠙ [2/5] RUN npm ci             ← vertex, or a ProgressGroup row
    ⠹ [3/5] COPY . .
```

Parent every vertex at the app's build stage, and insert exactly one intermediate level for
`ProgressGroup`: a group id becomes a stage under the build stage, ungrouped vertices hang directly
off the build stage. Do not reconstruct the LLB DAG from `Inputs`. Two reasons: BuildKit's own
renderer declined to, and `displayRows` indents two spaces per level with `maxTreeDepth = 64`, so a
30-deep LLB spine would be unreadable long before it was informative. `ProgressGroup` is the
producer's own statement about what constitutes one row, which is precisely what the stage tree
wants, including the `weak` rule: hold a group stage undeclared until a non-weak member starts.

### Event by event

| BuildKit | ocel |
| --- | --- |
| Vertex first seen | `StagePlanEvent{stages: [{id, parent_id: appBuildStage, title: Name}], final: false}` |
| `Started != nil` | `ProgressEvent{stage_id, message: Name}` — activates the row, starts the clock |
| `VertexStatus` tick | `ProgressEvent{stage_id, message: Name, current, total}` — draws the bar |
| `VertexLog` | see below |
| `Completed != nil`, `Error == ""` | `SpanEvent{span_id: stageID, parent_span_id: appBuildStage, name: Name, start: *Started, end: *Completed, status: OK}` |
| `Completed != nil`, `Error != ""` | same with `status: ERROR` → red `✗ <title> failed` |
| `Cached` | activate, then immediately span with `start == end` → commits as `✓ <title>  0s` |
| `VertexWarning` | unresolved, see §9 |

The cached case has a trap. `Renderer.StageEnd` returns early for a stage that is not in
`activeOrder` unless it failed, so a cached vertex that goes straight to a span without ever
emitting a `ProgressEvent` renders nothing at all. The adapter must activate every vertex,
including cached ones, before ending it.

### Counters

`ProgressEvent.current`/`total` are `uint32`; `VertexStatus.Current`/`Total` are `int64` and count
bytes for layer transfers, which routinely exceed 4 GiB. Rescale to KiB, or saturate, and say which
in the code. A vertex can also carry several concurrent `VertexStatus` ids while a stage has exactly
one pair of numbers: summing `Current` and `Total` across a vertex's statuses is the reading that
matches what a layer-pull bar means. `VertexStatus.Name` is the natural `ProgressEvent.message`.

### Logs

`VertexLog.Data` is a byte chunk, not a line. The adapter needs a per-vertex line buffer flushed on
`\n` — exactly Bazel's `stdoutLineBuffer` — and must strip ANSI and control bytes before anything
reaches a renderer that is doing its own `\033[%dA\033[J` cursor arithmetic. `providerkit`'s
`sanitizeMessage`/`stripControlChars` do this for provider events but sit on the wrong side of the
wire for a CLI-side build, so the sanitising has to be repeated.

Where the line then goes is the open decision for #621, in ascending cost:

- **File only, replay the tail on failure.** Build lines go to the run log via `BuildWriter`; the
  live row shows step name and elapsed. On a failed vertex, replay its retained tail (BuildKit's
  `printErrorLogs` over a 10-entry ring; Bazel's whole-block dump; turborepo's `errors-only`). No
  proto change. Trust-first: the log matters when it broke.
- **Tail in the row.** Fold the most recent line into `ProgressEvent.message`, which the row already
  renders as `— <message>`. One line of live context, zero new surface, everything but the tail
  lost.
- **`stage_id` on `LogEvent`.** Add `bytes stage_id = 2;` to `LogEvent`, keep a bounded per-stage
  ring in the renderer, and render the last N under the active row — progressui's `=> => #`
  sub-block. The most faithful, and the only one that needs a wire change and real renderer work.

These compose: ship the first, add the second as the live affordance, and the third only if a
build's interior genuinely needs to be readable while it runs.

### Attribution

No prefix scheme is needed. buildx prefixes `Vertex.Name` with `[target]` because its renderer is
single-stream and flat; ocel's tree already carries attribution by position, and
`AttrApp`/`ATTRIBUTE_KEY_APP` already tags the spans that land in `runtrace`. Set `AttrApp` on every
build span so the trace can group by app even though the terminal groups by indent.

---

## 9. What the mapping breaks

**`final` is global but a build subtree is never final.** `plan.final` is one bool that latches
forever (`p.apply` sets it, nothing clears it), and `rowLineLocked` only draws `(i/N)` markers once
it is set. A BuildKit vertex set is discovered incrementally and is complete only when the build
ends, so if the provider declares its plan final while the adapter is still adding vertices, build
rows will render `(3/47)` where 47 keeps growing — a counter that lies. Either `final` needs to
become per-subtree, or the build subtree must be excluded from sibling numbering, or the plan must
not be declared final until builds finish. This lands squarely on #621.

**`maxActiveRows = 20` is an admission cap, not a display cap.** Every tool surveyed recomputes its
visible set per frame and lets crowded-out work appear later. `ensureActive` instead refuses the
21st concurrent stage permanently and increments `droppedActive`, which is only ever surfaced as
`… and N more`. With `appConcurrency = 4` apps each running a BuildKit solve with ten-plus live
vertices, the cap is reached routinely and stages vanish from the live view for the rest of the
run. The fix has prior art in both directions: keep every active stage in the model and truncate at
draw time (BuildKit's `wrapHeight`, which additionally prefers in-progress rows over completed
ones), or sample the active set per frame by age (Bazel's `--ui_actions_shown` ordering). This is
the concrete defect for #622.

**Two clocks disagree.** The live elapsed comes from `n.started = p.now()`, set when the adapter
first activates the row; the committed duration comes from the span, carrying BuildKit's real
`Started`/`Completed`. A vertex that began before the adapter noticed will count up from a later
zero and then snap to a larger final number. Either seed the node's start from `Vertex.Started`
(needs a way to pass it — `ProgressEvent` has no timestamp field) or accept the discrepancy
knowingly.

**Verbose and live are mutually exclusive.** `live = human && !verbose && IsTerminal(w)` means
asking for build logs turns the tree off entirely, and `Renderer.Log` drops every line when not
verbose. There is no state today in which a stage tree and its logs coexist, which is the state
every surveyed tool actually ships.

**The non-TTY path has no throttle and no dedupe.** When `live` is false, `Renderer.Progress` prints
`message` per event. A BuildKit stream mapped naively emits a `ProgressEvent` per status tick per
vertex, so CI would receive thousands of near-identical lines. Every tool surveyed solves this
explicitly — Bazel floors the rate at 1 s and condenses to one line, turborepo flips to grouped,
BuildKit plain applies `minTimeDelta = 5s` / `minProgressDelta = 0.05` and a fairness scheduler.
ocel needs at minimum: print on state transition rather than on every tick, a rate limit, and a
stable per-job prefix.

**JSON mode cannot reconstruct the tree.** `Renderer.StagePlan` in `FormatJSON` emits only
`{"final":…,"count":…}` and discards the stages; `Renderer.StageEnd` returns immediately in JSON
mode. A JSON consumer therefore sees progress messages with `stageId` values it has no titles or
parents for, and never learns when a stage ended. BuildKit's answer — rawjson is the raw stream, and
every human view is derived from it — is the target shape, and ocel currently has it inverted.

**`VertexWarning` has no home.** `DegradedEvent{need, detail}` is the nearest message but its
vocabulary means "the deploy lacks a capability", not "your Dockerfile has a lint finding".
Emitting warnings as `LogEvent` loses the URL and source ranges. Leaving them for a post-build
summary is the cheapest correct answer and is what #621 should probably take.

---

## 10. Open questions handed to #621 and #622

For **#621** (build phases and docker build logs in the stage tree):

- Which of the three log placements above; whether `LogEvent` gains `stage_id`.
- Whether `final` becomes per-subtree, or the build subtree opts out of `(i/N)`.
- Whether cached vertices render as rows at all, or collapse into one `✓ 12 steps cached` line.
  BuildKit shows them individually with a `CACHED` prefix; at ocel's row budget that spends the
  whole live view on work that took no time.
- Whether the serverless path grows a synthetic build stage so both computes present one shape.

For **#622** (multi-app realtime and failure isolation):

- Per-frame recomputation of the visible row set, replacing the first-come admission cap.
- Whether N apps share one interleaved tree (today's model, and what BuildKit/compose do) or get
  turborepo's grouped buffer-and-replay — and whether CI flips that default automatically, as
  turborepo's `auto` and Bazel's non-curses path both do.
- Failure isolation has no prior art here worth copying wholesale: BuildKit cancels the solve,
  Bazel has `--keep_going`, turborepo has `--continue`. All three make it a flag rather than a
  fixed policy.
