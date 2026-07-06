# Changesets versioning setup

Status: ready-for-agent

## Parent

`.scratch/dev-build-release-workflow/PRD.md`

## What to build

Declarative, reviewable versioning across all publishable packages via changesets, enforcing lockstep versions.

- Add `@changesets/cli` as a root devDependency and initialize `.changeset/`.
- Configure `.changeset/config.json` with `baseBranch: "main"`, `access: "public"`, and a `fixed` group listing all 5 publishable package names (`ocel`, `@ocel/darwin-arm64`, `@ocel/darwin-x64`, `@ocel/linux-x64`, `@ocel/win32-x64`) so any changeset bumps all 5 together. `apps/web` is already `private` and excluded automatically.
- Add a root `ci:version` script that runs `changeset version` followed by a lockfile refresh (`pnpm install --lockfile-only`).

## Acceptance criteria

- [ ] `@changesets/cli` is a root devDependency and `.changeset/` is initialized
- [ ] `.changeset/config.json` sets `baseBranch: "main"`, `access: "public"`, and a `fixed` group covering all 5 publishable packages
- [ ] Authoring a changeset and running `ci:version` bumps all 5 packages to the same version in lockstep
- [ ] `apps/web` is not versioned by changesets
- [ ] `ci:version` refreshes the pnpm lockfile after bumping

## Blocked by

None - can start immediately
