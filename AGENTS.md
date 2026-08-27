## Rules

- Never auto-add an agent or AI name as a commit co-author.
- The code is the documentation — get context from it, and don't restate it. Prose may
  name what a human types, never what the code contains.
- Zero comments by default. Comment only to mark a gap — `TODO`/`FIXME` with a why.
  Doc-comments only under the literal paths `packages/` and `sdk/` — no other directory
  qualifies, however public its surface feels. An existing comment elsewhere is debt,
  not precedent: never match it, never extend it, delete it when touching nearby code.
- The commits are the ADRs. Rationale belongs in the commit message and PR bodies. Nowhere else.
- Do not generate changesets unless explicitly instructed.
- **Clean break** — TODO(alpha): remove when the first non-alpha version ships. Nothing
  is released, so nothing has consumers: replace old behaviour outright and delete the
  old path in the same diff. No shims, aliases, deprecated fallbacks, or migrations for
  unreleased state. Reviewers enforce this as the "Clean break" review rule.
- The code is the source of truth for memory too: write nothing to agent memory.
  Remembering is a user-initiated act — only an explicit "remember this" saves an entry.
- **Fix what you find** — an issue discovered mid-task gets exactly one of two
  dispositions: a fix, or a filed follow-up issue. "Out of scope" is not a disposition.
  An issue here is observed incorrectness — a bug, broken invariant, or security gap —
  not a style preference. Pick by measure, in order:
  1. The issue lives in a file this task already modifies → fix it now, and add a
     regression test.
  2. Elsewhere, and the fix is ≤50 changed lines (insertions + deletions of the fix
     itself, tests excluded) → fix it now, in its own commit.
  3. Anything larger → file a GitHub issue before the task ends: what you observed,
     where (`file:line`), why it is wrong — and link it from the PR body or report.
  A fix attempted under 1 or 2 that grows past 50 lines is reverted and filed under 3.

## About Ocel

**Ocel deploys apps to your own cloud.** Three pieces, each building on the one before —
the CLI stands alone; the SDK and console do not.

- **CLI** — deploys apps. Point it at a project and it builds, provisions and ships into
  your own provider account, with as little configuration as possible.
- **SDK** — adds infrastructure resources. Cloud primitives are function calls in app code,
  and the call _is_ the provisioning step, so there is no separate wiring to keep in sync.
  Proto-backed and language-neutral.
- **Console** — provides the UI over both.

**Not a cloud, and not managed anything.** Infrastructure lands in the customer's own account
under their billing and access; the hosted side is an optional control plane.

## Trust > Ops > Product > Polish

When they conflict:

- Trust = correctness + security + reliability (does the right thing, safely, always)
- Ops = simplicity, operability, maintenance cost (can one person run it)
- Product = DX, perceived performance, customer's cloud bill (what users feel)
- Polish = internal perf, your own spend, elegance (nice, never necessary)

## Codebase Map

Boundaries, not contents — every top-level directory is here, and a new one needs an
entry before it needs files. Dotfile directories are tooling and are exempt.

- **`packages/`** — everything published to npm, and nothing else. `@ocel/*` is public
  API; nothing internal may claim it.
- **`console/`** — Ocel's hosted control plane. Never call it a cloud.
- **`platform/<vendor>/`** — code targeting someone else's infrastructure. Each vendor
  holds its provisioning/deploy Go **and** the JS that runs on it. A second origin cloud
  lands here as a sibling. No import crosses from one vendor into another.
- **`platform/edge/`** — the edge role. `contract/` is what any edge must satisfy and what
  an edge and an origin agree on; both sides depend on it, neither owns it. Siblings are
  edges bought _independently of an origin cloud_ — a vendor's native edge belongs under
  that vendor instead.
- **`frameworks/<name>/`** — framework support, holding only what is **not** a branch of
  some host: shared protocol, the build-time adapter, and the host-neutral serving
  runtime a host drives through ports. Host-specific glue lives with the host.
- **`cli/`** — the `ocel` binary: Go internals plus the Node half that is bundled and
  embedded into it.
- **`sdk/`** — the Go SDK apps import to declare resources and talk to the dev server.
  Deliberately lean; never depends on the CLI.
- **`pkg/`** — small shared Go modules any module may depend on. They may import each other
  and `platform/edge/contract`, the one `platform/` path open to them, and nothing else in
  the repo — never a vendor SDK, the CLI, the SDK or the console.
- **`proto/`** — source of truth for the wire format. Bindings are **generated** — never
  hand-edit generated output.
- **`scripts/`** — development and release tooling, and the e2e harnesses.
- **`examples/`, `tests/`** — sample apps used as fixtures, and the dev-server suites
  that drive them. Suites that deploy live in `scripts/`, never here.
- **`docs/agents/`** — configuration the agent skills read. Not product documentation;
  nothing that explains the code belongs here.
- **`.github/`** — CI. **`.changeset/`** — the release mechanism; the workflow runs the
  version bump, never you.

## Agent skills

### Issue tracker: GitHub

Issues and specs live as GitHub issues on this repo; drive them with `gh`. When a
skill says "publish to the issue tracker" or "fetch the relevant ticket", that means
a GitHub issue here.

### Pull requests as a triage surface

**PRs as a request surface: no.**

When `yes`, PRs run through the same labels and states as issues. "External" means
`authorAssociation` of `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, or `NONE`.

### Wayfinding operations

Used by `/wayfinder`. The **map** is one issue labelled `wayfinder:map` (Notes /
Decisions-so-far / Fog body) with tickets as GitHub **sub-issues**, labelled
`wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Where sub-issues
aren't enabled: task list in the map body + `Part of #<map>` atop the child.

- **Blocking**: native issue dependencies — `POST .../issues/<child>/dependencies/blocked_by`
  takes the blocker's **database id** (`--jq .id`), not the `#number` or `node_id`.
  `issue_dependencies_summary.blocked_by` counts open blockers only — the live gate.
  Fallback: a `Blocked by: #<n>` line atop the child. Unblocked = every blocker closed.
- **Frontier**: open, unassigned children with no open blocker; first in map order wins.
- **Claim**: assign `@me` — the session's first write.
- **Resolve**: comment the answer, close, append a context pointer (gist + link) to
  the map's Decisions-so-far.

### Triage labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual label strings used in this repo's issue tracker.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding label string from this table.

Edit the right-hand column to match whatever vocabulary you actually use.

### Review rules

`.greptile/rules.md` is the set of gates every change must pass. A rule finding is
fixed or escalated to the human — those are the only two dispositions.
