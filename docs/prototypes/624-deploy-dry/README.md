# Prototype: `deploy --dry` and its apply, three ways

Throwaway. Branch `proto/624-deploy-dry`, never merged. Resolves nothing on its own —
it exists so [#624](https://github.com/ocelhq/ocel/issues/624) has something concrete to
argue with.

## What this is, and what it deliberately is not

The ticket asked for one provider end to end through the real CLI. It is built instead as
a **fidelity-reduced driver**: `cli/internal/cli/runui` is a fresh renderer, and `protocmd`
replays scripted event streams through it.

That trade buys the two things the real CLI cannot give today:

- **Tooling that does not exist yet.** BuildKit is not on `main` — [#570](https://github.com/ocelhq/ocel/issues/570)
  locked in-process Railpack + BuildKit but nothing is wired.
- **Three providers side by side**, including the AWS compute axis, without three live
  cloud accounts.

Nothing here talks to a provider, and the timings are invented. Every resource name, type
token and log line is drawn from what the real thing emits.

## The apply model: the app is the spine

The first cut of this prototype was **phase-major** — `Building`, `Provisioning` and
`Promoting` as the top-level rows, apps nested under them, everything interleaved by time
and correlated with `#N` prefixes. That is the SST shape, and it fails the one job that
matters: when something breaks, finding the output that explains it means scrolling and
correlating across the whole run.

It is now **app-major**. The observable unit is the **app**, because ocel is
application-centric — infra is expressed requirements of an app — and because the domain
model already says so: `naming.AppStack(env, app, release)` gives each app its own stack,
distinct from `Infra`. Phases are what an app passes through, not the other way round.

A run is a sequence of **units** — shared infrastructure, each app, the edge, promotion —
and each unit moves through **phases**. One `(unit, phase)` pair owns one **block**:

```
✓ web › building  24s
    ✓ [internal] load metadata for node:22-alpine  <1s CACHED
    Packages: +812
    ▲ Next.js 15.4.2
     ✓ Compiled successfully
    Route (app)                     Size     First Load JS
    ┌ ○ /                           1.2 kB        94.3 kB
    ├ ● /blog/[slug]                  842 B        92.1 kB
    └ λ /api/revalidate               128 B        87.4 kB
    ✓ [build 6/9] RUN pnpm build  18s
```

Blocks are **buffered and flushed whole** when their phase completes, so everything about
one app's phase is contiguous and indented under a header that names its path. The header
replaces `#N`: a path, not a correlation number. Apps are independent pipelines, so `web`
can be pushing while `api` is still building, and blocks from different apps interleave at
block granularity — never within a block.

Output is **complete**, not summarised. A successful deploy still prints the whole build
log, because a deploy can succeed and still be wrong: the `Route (app)` table above is how
you find out Next.js treated a page as static when you wanted ISR.

### The live view is one line per unit

On a TTY the buffered blocks would leave you staring at nothing, so above them sits a live
window: one row per unit in flight, and under it a single capped line of whatever that unit
is saying right now.

```
⠙ web › building  1s
    Generating static pages (28/28)
⠙ api › building  1s
    go: downloading github.com/ocelhq/ocel/sdk v0.4.1
```

It carries no information the blocks do not — everything is committed anyway — so it exists
purely as reassurance, which is what lets it collapse to one line. The line shows the latest
raw log when the phase has logs, and the structured progress message otherwise (during
provisioning you want `2 of 2 tasks healthy`, not raw engine chatter).

Consequence worth naming: **plain and live differ only by that window.** The committed
content is byte-identical, which makes "presentation degrades, content doesn't" literally
true rather than aspirational.

### Carriage returns collapse on ingest

pnpm and image pulls repaint one line thousands of times with `\r`. Replayed verbatim off a
TTY that is noise, not transparency, so only what a rewritten line finally said is kept.
`vps.apply.plain.txt` has one `Progress: resolved 1204 …` line where the raw stream carried
three.

## Run it

```
go build -o /tmp/protoui ./cli/internal/cli/runui/protocmd

/tmp/protoui --variant=vps --dry              # the plan, then stop
/tmp/protoui --variant=aws-container          # plan, then apply, live
/tmp/protoui --variant=aws-serverless --speed=0.5
/tmp/protoui --variant=aws-container --fail   # one app dies, promotion withheld
/tmp/protoui --variant=vps --max-rows=6       # squeeze the live window
/tmp/protoui --variant=vps | cat              # non-TTY: same blocks, no live window
/tmp/protoui --variant=vps --mode=json        # NDJSON
```

`captures/` holds the same runs recorded. `*.live.ansi` replay with `cat`.

## The three variants

**`vps`** — engine-less. The kit synthesizes CREATE/DELETE/KEEP from the same diff
`removeOrphans` executes, so there is no UPDATE of a live resource: a container changes by
replacement, and last deploy's container is an explicit DELETE row. Rows name docker and
systemd objects, not engine types. `pushing` is its own phase.

**`aws-container`** — a real Pulumi preview, so rows carry type tokens (`aws:ecs/Service`)
and UPDATE is available. The image push is an engine resource — `ocel:artifact/EcrImage`,
the custom resource fed the BuildKit digest — so it is a **plan row**, not progress, which
is [#618](https://github.com/ocelhq/ocel/issues/618)'s rule doing visible work.

**`aws-serverless`** — same engine, different compute axis. Artifacts are
`aws:s3/BucketObjectv2` rows, so one app contributes three upload rows plus two DELETEs for
the generation being reclaimed. The build interior is sparse — no vertices, just a Railpack
bundle — which is the contrast [#621](https://github.com/ocelhq/ocel/issues/621) predicted.

## What this revises on the map

- **[#622](https://github.com/ocelhq/ocel/issues/622)** — "no buffered replay anywhere" no
  longer holds. Worth stating precisely: this is not buffer-then-replay (holding the whole
  run to the end, which #617 found and #619 rejected) but **per-group incremental flush**,
  which is what turborepo does. Each block lands as its phase completes.
- **[#621](https://github.com/ocelhq/ocel/issues/621)** — "builds are ordinary top-level
  stages" no longer holds; a build is `web › building`. The ~200-line failure-only buffer
  is also gone: output is complete on success too.
- **[#619](https://github.com/ocelhq/ocel/issues/619)** — the lazy `#N` prefix dies with
  the numbering. Grouping replaces correlation.
- **[#617](https://github.com/ocelhq/ocel/issues/617)**'s deferred `ProgressGroup` hint is
  no longer needed for fan-out indexing: `(i/N)` is gone entirely, because the unit spine
  says what belongs together.

## Defects found while building it

1. **The plain projection dropped every log line** — the exact producer-rendered divergence
   #619 rules out.
2. **A parent rendered `✓` when its child failed.**
3. **Truncation counted escape bytes as columns**, so a coloured row wrapped earlier than a
   plain one and cut mid-word with no marker. `main` has the same bug in
   `deployui.truncateToWidth`.

All three are fixed here; the third is worth an implementation issue against `main`.

## Still open

- The row budget for the live window: units get two lines each, and beyond the budget the
  rest collapse to `+N more`. `--max-rows=6` is where it bites. Which units should survive
  the squeeze — the ones that started first, or the ones with the most left to do?
- Empty blocks. A phase that only carried a progress bar flushes as a header plus one
  synthesized line. Honest, or should such a phase not be a phase?
- Whether `promotion` and `cloudflare edge` deserve unit status, or are run-level steps that
  should read differently from an app.
