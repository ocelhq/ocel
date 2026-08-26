# Prototype: `deploy --dry` and its apply, three ways

Throwaway. Branch `proto/624-deploy-dry`, never merged. Resolves nothing on its own —
it exists so [#624](https://github.com/ocelhq/ocel/issues/624) has something concrete to
argue with.

## What this is, and what it deliberately is not

The ticket asked for one provider end to end through the real CLI. It is built instead as
a **fidelity-reduced driver**: `cli/internal/cli/runui` is a fresh renderer that implements
what the map already decided, and `protocmd` replays scripted event streams through it.

That trade buys the two things the real CLI cannot give today:

- **Tooling that does not exist yet.** BuildKit is not on `main` — [#570](https://github.com/ocelhq/ocel/issues/570)
  locked in-process Railpack + BuildKit but nothing is wired. Scripting the vertex stream
  is the only way to see what [#621](https://github.com/ocelhq/ocel/issues/621)'s decision
  actually looks like before the builder is written.
- **Three providers side by side**, including the AWS compute axis, without three live
  cloud accounts.

Nothing here talks to a provider, and the timings are invented. Every resource name, type
token and log line is drawn from what the real thing emits.

## Run it

```
go build -o /tmp/protoui ./cli/internal/cli/runui/protocmd

/tmp/protoui --variant=vps --dry              # the plan, then stop
/tmp/protoui --variant=aws-container          # plan, then apply, live
/tmp/protoui --variant=aws-serverless --speed=0.5
/tmp/protoui --variant=aws-container --fail   # one app dies, promotion withheld
/tmp/protoui --variant=vps --max-rows=8       # squeeze the row budget
/tmp/protoui --variant=vps --tail=0           # kill the build log tail
/tmp/protoui --variant=vps | cat              # the non-TTY projection
/tmp/protoui --variant=vps --mode=json        # NDJSON
```

`--speed` scales playback (`0` renders every frame with no waiting). `--tail` sets the log
lines kept under an active leaf. `--max-rows` is the live window's budget.

`captures/` holds the same runs recorded: `*.dry.txt`, `*.apply.plain.txt`, `*.ndjson`, and
`*.live.ansi` which you replay with `cat` (they carry the redraw escapes).

## The three variants

**`vps`** — engine-less. The kit synthesizes CREATE/DELETE/KEEP from the same diff
`removeOrphans` executes, so there is no UPDATE of a live resource: a container changes by
replacement, and last deploy's container is an explicit DELETE row. Rows name docker and
systemd objects, not engine types. Builds are BuildKit; the push to the host is its own
stage with a byte bar.

**`aws-container`** — a real Pulumi preview, so rows carry type tokens (`aws:ecs/Service`)
and UPDATE is available. The image push is an engine resource too — `ocel:artifact/EcrImage`,
the custom resource fed the BuildKit digest — so it is a **plan row**, not progress, which is
[#618](https://github.com/ocelhq/ocel/issues/618)'s rule doing visible work. During apply the
same push shows as a bar under its app.

**`aws-serverless`** — same engine, different compute axis. Artifacts are
`aws:s3/BucketObjectv2` rows, so one app contributes three upload rows plus two DELETEs for
the generation being reclaimed. The build interior is sparse — no vertices, just a Railpack
bundle — which is the contrast [#621](https://github.com/ocelhq/ocel/issues/621) predicted.

## Three defects this surfaced, already fixed here

1. **The plain projection dropped every log line.** Only the live tail rendered them, which
   is exactly the producer-rendered divergence [#619](https://github.com/ocelhq/ocel/issues/619)
   rules out. Plain now interleaves them as `#6 | <line>`.
2. **A parent rendered `✓` when its child failed.** `Provisioning` reported success over a
   dead `api`. It now renders `⚠ Provisioning  1 of 3 failed`.
3. **Truncation counted escape bytes as columns**, so a coloured row wrapped earlier than a
   plain one — and cut mid-word with no marker. `main` has the same bug in
   `deployui.truncateToWidth`.

## What I want you to react to

**Plan**

- Group heading is unsigiled (`app web  [nextjs]`) — the group is a container, not an action.
  On `main`'s bootstrap plan the group line carries its own sigil. Is dropping it right here?
- KEEP rows vanish into a faint `4 unchanged.` on the tally. Enough, or do you want them
  listed under a `Left in place:` block the way destroy does?
- Serverless shows every `BucketObjectv2` — 5 upload rows for two apps, and it will be more.
  Per-object rows, or one `3 artifacts` row per app that expands under `--verbose`?
- `(this one is slow)` on ECS services and the CloudFront invalidation — earns its room?
- Tally grammar: `10 to create, 2 to update, 1 to replace. 5 unchanged.`

**Apply**

- `(i/N)` now renders only on children, never roots — an index among sequential phases said
  nothing. But on children it is still the renderer *guessing* which siblings are one
  fan-out. This is the deferred `ProgressGroup` hint from [#617](https://github.com/ocelhq/ocel/issues/617)
  asking to come back. Do you want it now?
- Two concurrent builds means two 6-line tails on screen at once. Run `--tail=6` against
  `--tail=3` and `--tail=0`.
- The row budget splits evenly across active roots, then collapses what a subtree cannot fit
  into `+N more` at that subtree's depth. `--max-rows=8` is where it bites.
- `CACHED` on cached vertices — right word, right place?
- Every completed row scrolls into permanent history. A cached-heavy build writes a lot of
  `✓ … CACHED` lines nobody reads. Should cached vertices commit silently?

**Failure**

- `--fail` on both providers. The withheld-promotion line is the load-bearing sentence; the
  AWS one names what is still serving, the VPS one names what is stranded.
- Failure text is the only place a run points at `ocel run replay`.

**Non-TTY and JSON**

- `diff <(protoui --variant=vps --mode=plain --speed=0) captures/vps.apply.plain.txt` — the
  plain view is a pure function of the stream, which is [#619](https://github.com/ocelhq/ocel/issues/619)'s
  reconstructibility claim made testable.
- Bars print only at their ends off a TTY. Nothing else is throttled.
