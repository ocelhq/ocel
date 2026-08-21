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

## About Ocel

**Ocel deploys apps to your own cloud.** Three pieces, each building on the one before —
the CLI stands alone; the SDK and console do not.

- **CLI** — deploys apps. Point it at a project and it builds, provisions and ships into
  your own provider account, with as little configuration as possible.
- **SDK** — adds infrastructure resources. Cloud primitives are function calls in app code,
  and the call *is* the provisioning step, so there is no separate wiring to keep in sync.
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

### Issue tracker

Issues live as GitHub issues on `ocelhq/ocel`, driven by the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

The five canonical roles, each label string equal to its name. See `docs/agents/triage-labels.md`.

### Review rules

The rules every change is reviewed against. See `.greptile/rules.md`.