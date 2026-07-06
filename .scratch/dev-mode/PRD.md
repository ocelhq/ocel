# PRD: `ocel dev` and `ocel run`

Status: ready-for-agent

## Problem Statement

Today `ocel dev` is a stub that prints "not implemented yet" and starts a bare
RPC server whose `Declare` handler writes into an uninitialised map (it panics
in practice). There is no way to actually run an app against Ocel-managed
resources: a developer who writes `postgres("main")` in their code has no
mechanism that turns that declaration into a live connection string injected
into their running app. There is also no `ocel run` command for one-off tasks
(migrations, scripts) that need the same resources but not a long-lived dev
session. Developers currently cannot boot their app with real, ready
connections the way the Dev Mode docs promise.

## Solution

Flesh out `ocel dev` so that wrapping any dev command (`ocel dev -- next dev`)
resolves every declared resource to a real connection before the app boots,
and add `ocel run -- <cmd>` for one-off tasks that reuse the same resources
without a persistent server.

From the developer's perspective:

- They run `ocel init` once (out of scope here) to get an `ocel.config.ts`
  with a `projectId`.
- They run `ocel dev -- <their command>`. Ocel finds the config, checks they
  are logged in, discovers the resources declared under their `ocel/` folder,
  resolves each to a live instance, injects the connection info into the
  environment, and starts their command — streaming its output through.
- Running `ocel dev` in a second app of the same project "just works": the
  first process is the leader; the rest are followers that receive resolved
  connection info pushed from the leader and restart when it changes.
- Editing a resource file while `ocel dev` runs re-resolves resources and
  pushes updates to followers automatically.
- They run `ocel run -- drizzle-kit migrate` to execute a one-off command with
  the same resource connections injected, then exit.

Output is concise with elapsed timing, e.g.:

```
$ ocel dev -- next dev

✓ Resolved 1 resource: postgres("main")
✓ Connected — ready in 0.4s

  ▲ Next.js 15.0.0
  - Local:  http://localhost:3000
```

## User Stories

1. As a developer, I want `ocel dev` to locate my `ocel.config.ts` by walking up the directory tree, so that I can run it from any subdirectory of my project.
2. As a developer, I want a clear error telling me to run `ocel init` when no `ocel.config.ts` exists, so that I know how to fix an unconfigured project.
3. As a developer, I want my `ocel.config.ts` transpiled and executed to read its `projectId` and `discovery.paths`, so that configuration lives in TypeScript like the rest of my project.
4. As a developer, I want `ocel dev` to refuse to run and tell me to `ocel login` when I'm not authenticated, so that I understand why resource resolution can't proceed.
5. As a developer, I want every file under my `discovery.paths` imported so that each `postgres()`-style call self-registers, so that I don't have to manually list my resources.
6. As a developer, I want a sensible default discovery path (`ocel`) so that the common case needs no configuration.
7. As a developer in a monorepo, I want to point discovery at multiple globbed paths (e.g. `packages/*/ocel`) from a single root config, so that resources spread across packages are all discovered.
8. As a developer, I want all my declared resources sent for provisioning in one batch after discovery completes, so that resolution is atomic rather than piecemeal.
9. As a developer, I want each resolved resource injected as `OCEL_RESOURCE_<TYPE>_<id>` containing its connection info, so that the SDK's `getConfig` resolves it with no extra wiring.
10. As a developer, I want project-level environment variables fetched for my project injected alongside resource connections, so that my app has both its config and its connections.
11. As a developer, I want my app command run verbatim after `--` with the resolved environment, so that Ocel doesn't second-guess how my app starts.
12. As a developer, I want the leader to also run and inject into its own child app, so that the single-app case works without any follower concept.
13. As a developer running a second app of the same project, I want it to become a follower that receives connection info from the leader, so that both apps share the same resolved instances.
14. As a follower, I want the leader to push resolved environment to me as soon as I connect, so that my app boots with connections without repeating discovery.
15. As a follower, I want the leader to push updates when resources change and have my child restart, so that I stay in sync without manual intervention.
16. As a follower, I want a clear message and a non-zero exit when the leader disconnects, so that I know to restart the leader (no silent stale state).
17. As a developer, I want the leader to watch my resource files and re-resolve on change, so that adding a resource doesn't require a full restart.
18. As a developer, I want `ocel run -- <cmd>` to reuse a running leader's resolved environment when one exists, so that one-off tasks hit the same instances as my dev session.
19. As a developer, I want `ocel run` to work standalone (ephemeral resolution, no lockfile, torn down on exit) when no leader is running, so that one-off tasks work with or without an active dev session.
20. As a developer, I want `ocel run` to exit with my command's exit code, so that it composes with scripts and CI-like flows.
21. As a developer, I want concise progress output with elapsed time to "ready", so that I can see resolution happened and how long it took.
22. As a developer, I want a `--verbose` flag exposing the staged internals, so that I can debug when something goes wrong.
23. As a developer, I want discovery or bundling failures to fail fast without starting my app, so that I never boot against half-resolved connections.
24. As a developer, I want Ctrl+C to forward to my child, clean up the lockfile and follower streams, and exit, so that the next run starts clean.
25. As a developer, I want a stale/dead leader lockfile reclaimed automatically by the next `ocel dev`, so that a prior crash doesn't wedge my project.
26. As a developer, I want Ocel-internal build artifacts kept in a gitignored `.ocel/` folder, so that they don't pollute my repo.
27. As an SDK user, I want `defineConfig` exported from `ocel`, so that my config file is typed exactly as the docs show.

## Implementation Decisions

**TypeScript execution.** The Go CLI bundles the user's TypeScript
(`ocel.config.ts` and the generated discovery entrypoint) in-process using the
esbuild Go API (`github.com/evanw/esbuild/pkg/api`), writes the output under
`.ocel/`, and executes it with whatever `node` is on the user's PATH. No npm
esbuild dependency, no `npx`. The only hard runtime dependency is that `node`
exists.

**Config.** `ocel.config.ts` is resolved by walking up from the working
directory (tsconfig-style). Schema: `{ projectId: string (required);
discovery?: { paths?: string[] } }`, default `paths: ["ocel"]`. Missing or
unparseable config, or missing `projectId`, prints an `ocel init` instruction
and exits non-zero. `defineConfig` and the `OcelConfig` type are added as a new
root export `"ocel"` (the package currently only exports `./postgres`), backed
by a new `src/config.ts`.

**Canonical env var.** Standardize on `OCEL_DEV_SERVER` as the single variable
pointing the SDK/discovery entrypoint at the local dev server. The generated
entrypoint uses `OCEL_DEV_SERVER` (not `OCEL_SERVER`), and `defer.ts`'s error
message is corrected to match. The CLI sets `OCEL_DEV_SERVER` and
`OCEL_PHASE=discovery` when spawning the discovery child.

**Discovery.** The CLI globs every file under the resolved `discovery.paths`
and generates a single `.ocel/entry.mjs` with one bare side-effect import per
file, followed by `await Promise.all(globalThis.__ocelRegister)` and a
`POST /sync` to the dev server. Side-effect imports (not static analysis) are
what trigger resource registration.

**Declare + Sync split.** Per-resource registration stays a Connect RPC
(`ResourceService.Declare`) that accumulates into the leader's manifest
(replacing today's uninitialised `cache` map). `/sync` is a plain HTTP route
(not RPC): it signals "discovery done", and its handler builds the provision
payload (manifest + identity) and calls the provision function.

**Provision (stubbed).** A stubbed `fetchProjectConfig(ctx, apiURL, token,
projectId) (ProjectConfig, error)` returns `{OrgID, ProjectID, UserID, EnvVars
map[string]string}`. A stubbed provision call returns
`[]ProvisionedResource{Name, Type, Env map[string]string}`, where `Env`
already holds ready-to-inject `OCEL_RESOURCE_<TYPE>_<id>` -> JSON
`{connectionString}` entries. Both stubs keep their real signatures so the
feature works unchanged once the API lands. The CLI never provisions directly —
assignment is a warm-pool checkout owned by the (future) API.

**Env injection.** The child's environment is the merge, in increasing
precedence: inherited parent env < project `EnvVars` < resource `Env` entries.
Both the leader's own child and every follower's child receive the same merged
set.

**Auth.** Uses `credentials.Load()`; missing/expired credentials print a "run
`ocel login`" message and exit non-zero. No auto-login flow in `dev`/`run`.

**Leader/follower.** A lockfile in a per-user temp dir keyed by `projectId`
holds the leader's TCP address. No lock -> become leader (bind `:0`, record the
real address). Live lock -> follower. Dead lock (connection refused) -> reclaim
(remove, become leader); followers never self-promote. The leader also runs and
injects into its own child.

**Leader -> follower push.** A new proto package/service (`dev/v1 DevService`)
adds a server-streaming `Subscribe` RPC returning an `EnvUpdate`
(`map<string,string> env`), served on the leader's existing Connect server.
`ResourceService.Declare` is unchanged. On connect the leader pushes the full
resolved env to the follower; it pushes the full env again on every
re-resolve. Followers receive the full env (not a filtered subset), inject it,
and restart their child on updates. Requires regenerating code via `pnpm gen`.

**Watcher.** Only the leader watches `discovery.paths` (via
`github.com/fsnotify/fsnotify`). A debounced change triggers a full
re-discovery (re-bundle, re-execute; declares overwrite the manifest),
re-provision, and a push to all followers. Followers never watch or discover.

**`ocel run`.** A new command registered in `root.go`. If a leader lock exists,
it connects as a follower, takes the pushed env once, injects, runs the one-off
command, and exits with the child's code (no watcher). If no leader exists, it
spins an in-process ephemeral server on a random port for its own discovery +
sync, does not write a lockfile, runs the command, and tears everything down.

**Lifecycle.** SIGINT/SIGTERM is forwarded to the child; the leader closes
follower streams and removes the lockfile before exit. No framework/language
detection — the command after `--` is run verbatim.

**Output.** Default output is concise checkmark lines plus elapsed seconds to
"ready", then the child's stdout/stderr streamed through. A `--verbose` flag
exposes the staged internals (discovery, auth, sync, assign).

**New Go dependencies.** `github.com/evanw/esbuild` and
`github.com/fsnotify/fsnotify` added to `ocel/go.mod`.

## Testing Decisions

Out of scope for this PRD. The repo has no test harness today and the provision
path is deliberately stubbed; automated tests will be specified in a follow-up
once the provision API contract is real. Verification for this work is manual
plus `cd ocel && go build ./...` and `pnpm --filter ocel build` compiling
cleanly, and `pnpm gen` regenerating without error.

## Out of Scope

- The real Ocel provision/assign API — provisioning is stubbed behind stable
  signatures.
- `ocel init` (writing `ocel.config.ts` / `projectId`) — assumed to exist.
- Auto-login inside `dev`/`run` — users must `ocel login` first.
- Resource types beyond `postgres` (the proto/SDK support only Postgres today).
- Automated tests / test harness (see Testing Decisions).
- True incremental single-file re-scan (we do debounced full re-discovery).
- Per-follower filtered env (leader pushes the full env).

## Further Notes

- ADRs to record alongside this work: (1) esbuild Go API + user's `node` for TS
  execution; (2) leader/follower coordination via lockfile + Connect
  server-streaming push; (3) `Declare` (RPC) for registration + `/sync` (plain
  HTTP) as the provision trigger; (4) assign-from-warm-pool, CLI never
  provisions directly.
- Domain terms to capture in `CONTEXT.md`: Leader, Follower, Discovery,
  Manifest, Declare, Sync, Assign/Provision, Resource entry, `ocel run`.
- Known current bug this replaces: `dev.go`'s package-level `cache` map is
  never initialised and panics on first `Declare`.
