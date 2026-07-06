# Release pipeline (cross-compile + publish)

Status: ready-for-agent

## Parent

`.scratch/dev-build-release-workflow/PRD.md`

## What to build

The main-branch release pipeline that turns merged changesets into a published set of npm packages, cross-compiling all targets from a single Linux runner.

- Add `.github/workflows/release.yml` triggered on push to `main` (tags are a byproduct of `changeset tag`, not a trigger).
- `check-release` job determines whether unreleased changesets exist (counts `.changeset/*.md` not yet reflected in `.changeset/pre.json`).
- `version-pr` job (runs only when changesets are pending): runs `changesets/action` with the `ci:version` script to open/update the Version PR.
- `build-binaries` job (runs only when no changesets are pending, i.e. the Version PR was merged): matrix over the 4 targets, **all on a single `ubuntu-latest` runner**, each invoking `build-native.mjs` for its `GOOS`/`GOARCH` (with version injected via ldflags) and uploading the binary as an artifact.
- `publish` job: downloads all artifacts, places each into its native package's `bin/` (execute bit set on non-Windows), then runs `pnpm publish --access public --provenance` for `packages/ocel` and each of the 4 native packages, followed by `changeset tag`.
- Requires an `NPM_TOKEN` repository secret and `id-token: write` permission (for provenance), plus the write permissions needed to open the Version PR.

## Acceptance criteria

- [ ] `.github/workflows/release.yml` triggers on push to `main`
- [ ] A gate job detects pending changesets and branches between the Version-PR path and the build+publish path
- [ ] With pending changesets, the workflow opens/updates a Version PR via `changesets/action` + `ci:version`
- [ ] With no pending changesets, all 4 targets are cross-compiled on a single `ubuntu-latest` runner via `build-native.mjs` with the release version injected
- [ ] Built binaries are assembled into each native package's `bin/` with correct executable permissions before publish
- [ ] All 5 packages publish to npm with `--provenance` using the `NPM_TOKEN` secret, and `changeset tag` runs after a successful publish
- [ ] No manual `npm publish` step is required anywhere in the flow

## Blocked by

- `.scratch/dev-build-release-workflow/issues/01-native-binary-packages-resolve.md`
- `.scratch/dev-build-release-workflow/issues/03-changesets-versioning-setup.md`
