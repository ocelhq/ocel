# Contributing

These rules bind every change to this repository, whether a person or an agent makes it.
Review also checks every change against the gates in [`.greptile/rules.md`](.greptile/rules.md).

## Before you start

Open an issue with the [bug or feature form](https://github.com/ocelhq/ocel/issues/new/choose)
before any change bigger than a small fix, and agree on the approach there.

A maintainer closes a pull request without review when it:

- changes a [contract path](#contract-paths) from a contributor outside the maintainers,
  with no issue a maintainer agreed to;
- argues with a review gate instead of fixing the finding.

## Setup

`mise install` installs the versions `mise.toml` pins: Go, Node, Bun, golangci-lint, buf,
changie and act. pnpm installs the version `package.json` pins. The Python packages need
[uv](https://docs.astral.sh/uv/) and the Rust crates need cargo. Run Go in the environment
`mise.toml` sets (`mise activate`), which adds `-trimpath` to `GOFLAGS` as CI does.

Build what the Go tests embed:

```sh
pnpm install
pnpm turbo run build --filter=ocel
for dir in cli platform/aws/provider platform/gcp/provider platform/vps/provider \
  pkg/provider/transform; do
  go generate -C "$dir" ./...
done
```

CI on the pull request is the gate. To reproduce a failure it reports, `mise run check`
tests and lints the Go modules the change touches and every module that requires them,
runs biome on the changed files and the tests of the changed workspace packages, and runs
the Python and Rust tests when `python/` or `crates/` changed.

## Contract paths

Discuss a change to a contract path before you open a pull request. A contributor outside
the maintainers opens an issue and agrees the change there with a maintainer first. The
contract paths are:

- the `*.go` files directly in `pkg/provider`;
- `pkg/edge/`, `pkg/environment/`, `pkg/progress/`, `pkg/router/` and `platform/edge/contract/`;
- `proto/`;
- `packages/`, `sdk/`, `python/` and `crates/`;
- `AGENTS.md`, `CLAUDE.md`, `GLOSSARY.md`, `.greptile/rules.md` and this file.

A contract change updates every implementer in the same pull request
([Clean break](.greptile/rules.md#clean-break)), and the conformance suites in
`pkg/provider/conformance` pass.

## User-visible changes

A change users can see (CLI output or flags, config, an SDK API, the console, the docs)
fills in **What users see** in the pull request and adds a changelog entry with
`changie new`.

TODO(alpha): no changie entries until the first release; remove this line when releases start.

## Naming

Names follow [Naming](.greptile/rules.md#naming): its table applies to every name, and
rules 1–9 to `pkg/` and the vendors that implement the provider contract.

## Structure

- Code goes where the [codebase map](AGENTS.md#codebase-map) puts it. A new top-level
  directory gets its map entry before it gets files.
- A package sits under `pkg/provider/` only if nothing but providers uses it.
- A vendor keeps each port, hook or hook group in its own file
  ([Naming](.greptile/rules.md#naming) rule 6).
- [Clean break](.greptile/rules.md#clean-break) applies to every change.

## Comments

The code is the documentation: read it for context, and don't restate it. Prose may name
what a human types, never what the code contains.

Comments follow [Signal](.greptile/rules.md#signal). Its doc-comment exception covers the
four paths it names and no other, however public another directory's API looks. Never copy
or extend a comment Signal does not allow, and delete it when you change the code near it.

## Testing

- Write a failing test first, then the code that makes it pass.
- A bug fix adds a regression test that fails without the fix.
- Test names follow the Test row of [Naming](.greptile/rules.md#naming).
- CI runs in tiers. A pull request runs Lint, Unit Tests, and the Smoke Tests of each target
  its paths reach; a push to main and a release pull request also run Integration Tests and
  E2E Tests, and a release pull request E2E Tests (Cloud).
- A Go test that drives docker, an emulator or a VM builds only under the `integration` tag,
  and fails rather than skips where that environment is missing.
- A Go test that drives docker takes its networks from `enginetest.Network(t, role)`, which
  every run reuses, and labels its containers with `enginetest.RunLabelArgs(t)`. Every
  network created and removed is a host bridge whose address coming and going reads to
  browsers on the machine as the network changing, failing their requests.
- The VM and emulator suites (`scripts/incus.sh`, `scripts/incus-fanout.sh`,
  `scripts/floci.sh` and the `scripts/act.sh` replays) run in CI. Run one locally only to
  reproduce a failure CI reported.
- Suites that need cloud credentials (the `e2e:cloud` label, which runs E2E Tests (Cloud)
  once on a pull request, and the nightly run) are run by maintainers.

## Fix what you find

A finding is observed incorrectness or a broken gate in `.greptile/rules.md`; a preference is
not one. State only an unreleased build could have written is never a finding (Clean break).
Each finding gets one disposition:

1. **Fix it** when it is in a file this change modifies, or the fix is 50 changed lines or
   fewer (tests excluded) and touches no contract path this change does not. A fix elsewhere
   is its own commit. A behaviour fix adds a regression test that fails without it.
2. **File it** only if it is a verified Trust issue (correctness, security, reliability) too
   large to fix. Verified means a failing test or a concrete `file:line` trace to the wrong
   result. Search open issues first; comment on a match instead of filing.
3. **Call it out** otherwise: unverified, too large and not about Trust, or needing a
   maintainer ruling. One line with `file:line` under **Found, not fixed** in the pull
   request, or in the report when there is no pull request. Never filed.

A fix that grows past 50 lines is reverted and goes to 2 or 3.

## Commits

- [Conventional Commits](https://www.conventionalcommits.org), checked by commitlint
  (`commitlint.config.mjs`). The header is at most 120 characters.
- The subject says what is true after the change, not what you did:
  `fix(cli): deploy reads ocel.json from the project root`.
- The body gives the rationale. Commit messages and pull request bodies are the
  decision records, and no file in the repository repeats their rationale.
- Every commit builds. The checks run on the tip of the pull request.
- No agent or AI co-author or attribution lines.

## Pull requests

Fill in the [template](.github/pull_request_template.md). A pull request is done when
review has no open finding against the gates, or a maintainer has ruled on each one
escalated: a finding is fixed or escalated, never waived
([Disposition](.greptile/rules.md#disposition)).

## Security

Report a vulnerability as [SECURITY.md](SECURITY.md) describes, never in a public issue.
