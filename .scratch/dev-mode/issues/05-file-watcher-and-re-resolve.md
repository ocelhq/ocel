# File watcher & re-resolve

Status: ready-for-agent

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

Let the leader react to resource changes without a restart. The leader (and
only the leader) watches `discovery.paths` with a file watcher (fsnotify). A
debounced change triggers a full re-discovery (re-bundle the entrypoint,
re-execute; declares overwrite the manifest), a fresh provision call, and a
push of the updated env to all connected followers. Followers restart their
child process to pick up the new environment.

Add the fsnotify Go dependency to `ocel/go.mod`.

## Acceptance criteria

- [ ] The leader watches all resolved `discovery.paths`; followers do not watch.
- [ ] A change under a watched path triggers a single debounced full re-discovery + re-provision.
- [ ] Updated env is pushed to all connected followers over the existing `Subscribe` stream.
- [ ] Followers restart their child process when a new env push arrives.
- [ ] Adding a new resource file is picked up without restarting the leader.

## Blocked by

- `.scratch/dev-mode/issues/04-leader-follower-coordination-and-push.md`
