# Leader/follower coordination + push

Status: ready-for-agent

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

- [ ] `pnpm gen` regenerates cleanly and `DevService.Subscribe` exists in generated Go + TS.
- [ ] First `ocel dev` for a project becomes leader and writes a lockfile containing its address.
- [ ] A second `ocel dev` for the same project becomes a follower and does not repeat discovery.
- [ ] A follower receives the full resolved env pushed by the leader and boots its child with it.
- [ ] A dead/stale lockfile is reclaimed by the next `ocel dev`, which becomes leader.
- [ ] On leader disconnect, the follower stops its child, prints a restart message, and exits non-zero.

## Blocked by

- `.scratch/dev-mode/issues/03-single-app-happy-path.md`
