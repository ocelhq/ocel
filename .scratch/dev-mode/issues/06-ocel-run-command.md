# `ocel run` command

Status: ready-for-agent

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

Add `ocel run -- <cmd>` for one-off tasks (e.g. `ocel run -- drizzle-kit
migrate`) that need the same resource connections but not a persistent dev
session. Register it as a new command.

- If a leader lock exists for the project, connect as a one-shot follower, take
  the pushed resolved env once, inject it, run the command, and exit with the
  child's exit code. No file watching.
- If no leader exists, perform a standalone ephemeral resolution: spin an
  in-process server on a random port for its own discovery + sync, inject the
  resolved env, run the command, and tear everything down on exit. Do NOT write
  a lockfile (it must not advertise itself as a leader).

## Acceptance criteria

- [ ] `ocel run -- <cmd>` with a running leader reuses the leader's resolved env and hits the same instances.
- [ ] `ocel run -- <cmd>` with no leader resolves standalone, runs, and tears down without leaving a lockfile.
- [ ] `ocel run` never starts a file watcher.
- [ ] The command runs verbatim with the merged env and the CLI exits with the child's exit code.

## Blocked by

- `.scratch/dev-mode/issues/03-single-app-happy-path.md`
- `.scratch/dev-mode/issues/04-leader-follower-coordination-and-push.md`
