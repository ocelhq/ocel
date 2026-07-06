# UX & lifecycle polish

Status: ready-for-agent

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

Round out the developer experience across `ocel dev` and `ocel run`:

- Concise default output: checkmark lines summarising resolved resources and a
  "ready in Ns" elapsed timing, then hand off to the child's streamed output.
  Example:

  ```
  ✓ Resolved 1 resource: postgres("main")
  ✓ Connected — ready in 0.4s
  ```

- A `--verbose` flag that exposes the staged internals (discovery, auth, sync,
  assign) for debugging.
- Signal handling: SIGINT/SIGTERM is forwarded to the child; the leader closes
  follower streams and removes its lockfile before exiting, so the next run
  starts clean.

## Acceptance criteria

- [x] Default output shows resolved resources and elapsed "ready" time, then streams child output.
- [x] `--verbose` surfaces the staged internal steps.
- [x] Ctrl+C forwards to the child and the process exits promptly.
- [ ] On exit, the leader removes its lockfile and closes follower streams (no stale lock left behind on clean exit).

## Blocked by

- `.scratch/dev-mode/issues/03-single-app-happy-path.md`

## Progress note (2026-07-06)

Merged `ocel/issue-single-app-happy-path` (issue 03) into this branch as a
base — this worktree started from the pre-dev-mode stub with no `03` code at
all, so the UX polish had nothing real to sit on top of.

Implemented in `ocel/internal/cli/dev.go` (+ `dev_test.go`):

- Concise default output: `✓ Resolved N resource(s): postgres("main")` and
  `✓ Connected — ready in Ns`, timed from the start of `runDev`, before
  streaming the child's stdout/stderr.
- `--verbose` flag prints `→ ...` lines for each staged internal step
  (auth check, config resolution, dev server start, discovery, running the
  discovery entrypoint, sync/provision) in addition to the concise lines.
- SIGINT/SIGTERM forwarding: `runChildForwardingSignals` registers a signal
  channel before starting the child, forwards any received signal to the
  child process, and returns as soon as the child exits — verified with a
  real `sh` child trapping SIGTERM and with a manual smoke test
  (`ocel dev -- <child>` + `kill -TERM`) exiting with the child's own code.

**Not implemented — last acceptance criterion:** "the leader removes its
lockfile and closes follower streams" assumes the leader/follower + lockfile
machinery from issue 04
(`.scratch/dev-mode/issues/04-leader-follower-coordination-and-push.md`).
That issue's branch (`ocel/issue-leader-follower-coordination-and-push`) has
no commits beyond the common base — it hasn't been started, even though this
issue's own "Blocked by" section only lists 03. There is no lockfile and no
follower concept in the codebase to clean up yet. What exists today (the
single-app case) does get a clean shutdown: the local dev HTTP server is
always closed via `defer httpSrv.Close()` in `runDev`, including on the
signal-forwarded exit path. Once issue 04 lands, its lockfile-removal and
follower-stream-closing should be added to the same shutdown path (e.g.
alongside or via the same `defer` sequence in `runDev`/wherever the leader
loop ends up living) — flagging this as the integration point for whoever
picks up 04 or does the final `feature/dev-mode` merge.

Verification: `cd ocel && go build ./... && go vet ./... && go test ./... -race`
all pass; manually ran the built binary against a fixture project and
confirmed the exact output format from the issue/PRD example, `--verbose`
staging output, and Ctrl+C/SIGTERM forwarding with exit-code propagation.
