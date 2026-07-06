# Single-app happy path: discovery → sync → inject → spawn

Status: ready-for-agent

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

- [ ] Not-logged-in exits non-zero with a `ocel login` instruction; no app started.
- [ ] All files under `discovery.paths` are imported and their resources land in the manifest.
- [ ] `/sync` triggers exactly one batched provision call carrying the manifest + org/project/user identity.
- [ ] Resolved resources are injected as `OCEL_RESOURCE_<TYPE>_<id>` JSON, alongside project env vars, at the documented precedence.
- [ ] The `--` command runs verbatim with the merged env, streams output, and the CLI exits with the child's exit code.
- [ ] A discovery/bundle failure prevents app start and exits non-zero.
- [ ] The stubbed `fetchProjectConfig` and provision function have their final signatures.

## Blocked by

- `.scratch/dev-mode/issues/01-sdk-config-api-and-env-canonicalization.md`
- `.scratch/dev-mode/issues/02-config-resolution-and-ts-execution.md`
