# Contributing

These rules bind every change to this repository, whether a person or an agent makes it.
Review also checks every change against the gates in [`.greptile/rules.md`](.greptile/rules.md).

## Before you start

Open an issue with the [bug or feature form](https://github.com/ocelhq/ocel/issues/new/choose)
before any change bigger than a small fix, and agree on the approach there.

A maintainer closes a pull request without review when it:

- changes a [contract path](#contract-paths) and its author is not a maintainer;
- argues with a review gate instead of fixing the finding.

## Contract paths

The contract is the set of paths [`scripts/contract-paths.mjs`](scripts/contract-paths.mjs)
lists: the provider and edge contracts, the wire format, every published package, and the
rules in this file, `AGENTS.md` and `.greptile/rules.md`. For now only maintainers change
them, and CI fails a pull request from anyone else that touches one.

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
  directory or package gets its map entry before it gets files.
- A package sits under `pkg/provider/` only if nothing but providers uses it.
- A vendor keeps each port, hook or hook group in its own file
  ([Naming](.greptile/rules.md#naming) rule 6).
- Nothing is released yet, so a change replaces old behaviour outright
  ([Clean break](.greptile/rules.md#clean-break)).

## Comments

The code is the documentation. Prose may name what a human types, never what the code
contains.

Comments follow [Signal](.greptile/rules.md#signal). Doc-comments belong only under
`packages/`, `sdk/`, `python/` and `crates/`, however public another directory's API looks.
A comment anywhere else is debt: never match or extend it, and delete it when you change
the code near it.

## Testing

- Write a failing test first, then the code that makes it pass.
- A bug fix carries a regression test that fails without the fix.
- Test names follow the Test row of [Naming](.greptile/rules.md#naming).
- CI runs every workflow in `.github/workflows/` whose paths a change touches.
  `scripts/act.sh` replays the pull request gates locally.
- Suites that need cloud credentials (the `journey:real` label, the nightly run) are run by
  maintainers.

## Fix what you find

An issue found mid-task gets one of two dispositions: a fix, or a filed follow-up issue.
"Out of scope" is not one. An issue is observed incorrectness (a bug, a broken invariant,
a security gap), not a style preference. Pick by measure, in order:

1. The issue is in a file this change already modifies: fix it now, and add a regression
   test.
2. It is elsewhere, and the fix is 50 changed lines or fewer (insertions plus deletions of
   the fix itself, tests excluded): fix it now, in its own commit.
3. Anything larger: before the task ends, file a GitHub issue unless one exists, saying
   what you observed, where (`file:line`) and why it is wrong, and link it from the pull
   request. A fix begun under 1 or 2 that grows past 50 lines is reverted and filed
   under 3.

A name that breaks the naming rules is an issue, not a style preference. In a file you
already change, rename it; elsewhere the ≤50-line rule applies; beyond that, file it.

## Commits

- [Conventional Commits](https://www.conventionalcommits.org), checked by commitlint
  (`commitlint.config.mjs`).
- The subject says what is true after the change, not what you did:
  `fix(cli): deploy reads ocel.json from the project root`.
- The body carries the rationale. Commit messages and pull request bodies are the
  decision records: rationale lives there and nowhere else.
- Every commit builds and passes its checks.
- No agent or AI co-author or attribution lines.

## Pull requests

Fill in the [template](.github/pull_request_template.md). A pull request is done when
review has no open finding against the gates, or a maintainer has ruled on each one
escalated: a finding is fixed or escalated, never waived
([Disposition](.greptile/rules.md#disposition)).

## Security

Report a vulnerability as [SECURITY.md](SECURITY.md) describes, never in a public issue.
