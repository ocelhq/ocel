# Single-app happy path: discovery → sync → inject → spawn

Status: done

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

The core end-to-end path for `ocel dev -- <cmd>` in the single-app
(leader-only) case, with provisioning stubbed:

1. Resolve config (from the prior slice) and verify the user is authenticated
   via stored credentials. If not logged in, print a "run `ocel login`" message
   and exit non-zero.
2. Start the local dev server (Connect `ResourceService`) and glob every file
   under `discovery.paths`, generating a single `.ocel/entry.mjs` with one
   side-effect import per file, followed by awaiting the collected registration
   promises and a `POST /sync`.
3. Run the entrypoint with `node`, setting `OCEL_PHASE=discovery` and
   `OCEL_DEV_SERVER=<local server address>`. Each resource's `Declare` RPC
   accumulates into a real in-memory manifest (this replaces the currently
   uninitialised `cache` map that panics on first call).
4. On `POST /sync`, build the provision payload (manifest + identity from a
   stubbed `fetchProjectConfig(ctx, apiURL, token, projectId)` returning
   `{OrgID, ProjectID, UserID, EnvVars}`) and call a stubbed provision function
   returning `[]ProvisionedResource{Name, Type, Env}` where `Env` already holds
   ready-to-inject `OCEL_RESOURCE_<TYPE>_<id>` → JSON `{connectionString}`
   entries. Both stubs keep their real signatures so the feature works
   unchanged once the API lands.
5. Merge the resolved environment — inherited parent env < project `EnvVars` <
   resource `Env` entries — and spawn the command after `--` verbatim,
   streaming its stdout/stderr and exiting with its code.

Fail fast: if bundling or the discovery child fails, surface the error and do
NOT start the app; exit non-zero.

## Acceptance criteria

- [x] Not-logged-in exits non-zero with a `ocel login` instruction; no app started.
- [x] All files under `discovery.paths` are imported and their resources land in the manifest.
- [x] `/sync` triggers exactly one batched provision call carrying the manifest + org/project/user identity.
- [x] Resolved resources are injected as `OCEL_RESOURCE_<TYPE>_<id>` JSON, alongside project env vars, at the documented precedence.
- [x] The `--` command runs verbatim with the merged env, streams output, and the CLI exits with the child's exit code.
- [x] A discovery/bundle failure prevents app start and exits non-zero.
- [x] The stubbed `fetchProjectConfig` and provision function have their final signatures.

## Blocked by

- `.scratch/dev-mode/issues/01-sdk-config-api-and-env-canonicalization.md` (merged into this branch)
- `.scratch/dev-mode/issues/02-config-resolution-and-ts-execution.md` (merged into this branch)

## Implementation notes

- Merged `ocel/issue-sdk-config-api-and-env-canonicalization` and
  `ocel/issue-config-resolution-and-ts-execution` into this branch first —
  neither had landed on `feature/dev-mode` yet and this issue depends on
  both.
- Added `projectconfig.Config.Dir` (not in the original 02 scope) since
  `discovery.paths` must resolve relative to the config file's directory,
  not the CLI's working directory.
- New packages: `internal/manifest`, `internal/provision`,
  `internal/discovery`, `internal/devserver`. `internal/cli/dev.go` wires
  them together; `ExitError` (+ `main.go` handling) propagates the child's
  real exit code instead of always exiting 1.
- **Flagging, not fixing (out of scope for this issue):** `packages/ocel/src/postgres/index.ts`'s
  `postgres()` factory calls `pg.__config()` (which reads
  `OCEL_RESOURCE_POSTGRES_<id>` from `process.env` and throws if unset)
  unconditionally, even when `OCEL_PHASE=discovery`. Since discovery files
  are executed in a separate node process before any resource env vars
  exist, a discovery file that calls the real `postgres("id")` (as shown
  in the PRD's own example) will currently throw during discovery. My
  tests exercise the CLI/devserver path with a fixture that declares a
  resource directly (mirroring what `Postgres`'s constructor + `defer`
  does) rather than through the real `postgres()` factory, so this repo's
  Go changes are unaffected — but real end-to-end usage with the SDK as
  written will hit this. Likely needs a follow-up in the SDK to skip Pool
  construction when `OCEL_PHASE === "discovery"`.
