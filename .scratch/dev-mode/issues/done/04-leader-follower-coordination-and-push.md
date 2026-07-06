# Leader/follower coordination + push

Status: done

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

Make `ocel dev` work when run for the same project more than once, using a
leader/follower model with a push channel.

1. Add a new proto package/service `dev/v1 DevService` with a server-streaming
   `Subscribe` RPC returning an `EnvUpdate` (`map<string,string> env`), served
   on the leader's existing Connect server. Regenerate code via `pnpm gen`.
   `ResourceService.Declare` is unchanged.
2. Leader election via a lockfile in a per-user temp dir keyed by `projectId`,
   holding the leader's TCP address (server binds a dynamic `:0` port, records
   the real address). No lock → become leader. Live lock → become follower.
   Dead lock (connection refused) → reclaim it (remove, become leader);
   followers never self-promote.
3. On connect, the leader pushes the full resolved env to the follower. The
   follower injects it and spawns its own child app (no discovery of its own).
4. If the leader disconnects, the follower stops its child, prints a message to
   restart the leader, and exits non-zero.

The leader also continues to run and inject into its own child (from the prior
slice).

## Acceptance criteria

- [x] `pnpm gen` regenerates cleanly and `DevService.Subscribe` exists in generated Go + TS.
- [x] First `ocel dev` for a project becomes leader and writes a lockfile containing its address.
- [x] A second `ocel dev` for the same project becomes a follower and does not repeat discovery.
- [x] A follower receives the full resolved env pushed by the leader and boots its child with it.
- [x] A dead/stale lockfile is reclaimed by the next `ocel dev`, which becomes leader.
- [x] On leader disconnect, the follower stops its child, prints a restart message, and exits non-zero.

## Blocked by

- `.scratch/dev-mode/issues/03-single-app-happy-path.md`


## Progress note (2026-07-06)

Agent started and executed for a while but the execution failed before completion. The worktree and branches already exist but may be outdated.

## Completion note (2026-07-07)

Implemented in full via RGR across 5 commits on
`ocel/issue-leader-follower-coordination-and-push`:

1. `proto/dev/v1/dev.proto` (`DevService.Subscribe`), regenerated Go + TS.
2. `ocel/internal/lockfile`: read/write/remove a per-project leader-address
   lockfile under a per-user temp dir.
3. `ocel/internal/election`: `Elect(projectID)` — no lockfile → Leader; a
   reachable recorded address → Follower; unreachable → reclaim → Leader.
4. `ocel/internal/devserver`: `Subscribe` handler + `PushEnv`, which caches
   the latest env and delivers it to a subscriber immediately on connect as
   well as to every already-connected one.
5. `ocel/internal/cli/dev.go`: `runDev` now calls `election.Elect` first and
   branches into `runLeader` (previous single-app flow, plus writing/removing
   the lockfile and calling `srv.PushEnv` right after its own sync) or
   `runFollower` (connects, waits for the first push, spawns its child, and
   on stream-close kills the child's whole process group, prints a restart
   message, and exits non-zero).

Key decisions:
- The lockfile is scoped by uid under `os.TempDir()` (not a single shared
  path) to avoid cross-user collisions/permission issues on shared `/tmp`.
- The follower's child runs in its own process group
  (`procgroup_unix.go`/`procgroup_other.go`) so a disconnect-triggered kill
  reaches real child trees (e.g. a dev server that forks), not just the
  immediate process — this also fixed a test hazard where `sh -c` spawns a
  detached grandchild that outlives a plain `Process.Kill()`.
- `devserver.Server.PushEnv` caches the latest env so a follower connecting
  after the leader has already resolved once still gets it immediately,
  without needing a separate "replay" path.
- `resolvedEnv`/`applyEnv` factor the leader's existing `mergeEnv` map-merge
  logic out so the exact same map is both pushed to followers and applied to
  the leader's own child.

Note found during setup: this worktree was missing `go.work` (gitignored,
not copied by the orchestrator) — `go build`/`pnpm gen` failed with "no such
tool" until it was recreated locally (`go 1.25.3` / `use ./ocel`, matching
the primary checkout). Not committed (still gitignored); future agents in
fresh worktrees for this repo will likely hit the same thing.

Out of scope (left for later issues per the PRD): the file watcher /
re-resolve-on-change push (issue 05) and `ocel run` (issue 06) — the
`Subscribe` stream and `PushEnv` broadcast are already shaped to support
repeated pushes, so 05 shouldn't need to change this issue's plumbing.
