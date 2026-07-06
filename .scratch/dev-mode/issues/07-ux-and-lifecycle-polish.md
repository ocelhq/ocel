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

- [ ] Default output shows resolved resources and elapsed "ready" time, then streams child output.
- [ ] `--verbose` surfaces the staged internal steps.
- [ ] Ctrl+C forwards to the child and the process exits promptly.
- [ ] On exit, the leader removes its lockfile and closes follower streams (no stale lock left behind on clean exit).

## Blocked by

- `.scratch/dev-mode/issues/03-single-app-happy-path.md`
