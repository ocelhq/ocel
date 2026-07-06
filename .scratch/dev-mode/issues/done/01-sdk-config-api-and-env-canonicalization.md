# Prefactor — SDK config API + `OCEL_DEV_SERVER` canonicalization

Status: done

## Parent

`.scratch/dev-mode/PRD.md`

## What to build

Prepare the `ocel` npm package for dev mode. Two changes:

1. Add a typed configuration API so `ocel.config.ts` can be authored as the
   docs show: `import { defineConfig } from "ocel"`. Introduce an `OcelConfig`
   type (`{ projectId: string; discovery?: { paths?: string[] } }`) and a
   `defineConfig` helper, exposed via a new **root** package export (`ocel`).
   The package currently only exports `./postgres`.
2. Standardize the local dev-server env var on `OCEL_DEV_SERVER` throughout the
   SDK. The value is already read under that name; correct the mismatched error
   message in `defer` (which currently reports `OCEL_SERVER`) so the SDK speaks
   one name only.

No CLI changes in this slice.

## Acceptance criteria

- [x] `import { defineConfig, type OcelConfig } from "ocel"` type-checks and returns its input typed.
- [x] `OcelConfig` requires `projectId` and allows optional `discovery.paths: string[]`.
- [x] The package's `exports` map serves the new root entry alongside `./postgres`.
- [x] The SDK references only `OCEL_DEV_SERVER` (no lingering `OCEL_SERVER` string, including in error messages).
- [x] `pnpm --filter ocel build` compiles cleanly.

## Blocked by

None - can start immediately.
