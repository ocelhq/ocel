CONTRIBUTING.md is binding for agents; read it before any change.

@CONTRIBUTING.md

## Memory

The code is the source of truth for memory too: write nothing to agent memory.
Remembering is a user-initiated act — only an explicit "remember this" saves an entry.

## About Ocel

**Ocel deploys apps to your own cloud.** Three pieces, each building on the one before —
the CLI works on its own; the SDK and console do not.

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

Boundaries, not contents. Every top-level directory is here except dotfile directories,
which are tooling.

- **`packages/`** — everything published to npm, and nothing else. `@ocel/*` is public
  API; nothing internal may claim it.
- **`ui/<surface>/`** — a React surface more than one product renders. Published nowhere,
  so it may not live in `packages/`; depended on by the CLI's node half and by the
  console, and depending on neither.
- **`ui/theme/`** — the tokens and register dials every surface renders in, `www`
  included, and the `DESIGN.md` that governs them. No React; depends on nothing.
- **`console/`** — Ocel's hosted control plane. Never call it a cloud.
- **`platform/<vendor>/`** — code targeting someone else's infrastructure. Each vendor
  has its provisioning/deploy Go **and** the JS that runs on it. A second origin cloud
  lands here as a sibling. No import crosses from one vendor into another.
- **`platform/edge/`** — the edge role. `contract/` is what any edge must satisfy and what
  an edge and an origin agree on; both sides depend on it, neither owns it. Siblings are
  edges bought _independently of an origin cloud_ — a vendor's native edge belongs under
  that vendor instead.
- **`platform/s3/`** — the S3 protocol as a store any origin can reach with a static
  credential: the plain-S3 bucket backend and its in-bucket upload sessions. The one
  `platform/` path every vendor may import, and it imports none of them.
- **`frameworks/<name>/`** — framework support, containing only what is **not** a branch of
  some host: shared protocol, the build-time adapter, and the host-neutral serving
  runtime a host drives through ports. Host-specific glue lives with the host.
- **`frameworks/node/`** — the `<name>` that is a runtime rather than a framework. It takes
  what more than one host must run to serve a plain node function, and nothing that knows
  which host is running it: a host's own entry, packaging and paths stay with that host.
- **`cli/`** — the `ocel` binary: Go internals plus the Node half that is bundled and
  embedded into it.
- **`sdk/`** — the Go SDK apps import to declare resources and talk to the dev server.
  Deliberately lean; never depends on the CLI.
- **`python/`** — the uv workspace of everything published to PyPI, and nothing else.
  `ocel` is public API.
- **`crates/`** — the cargo workspace of everything published to crates.io, and nothing
  else. `ocel` is public API.
- **`pkg/`** — one Go module of shared packages any module may depend on;
  `provider/pulumi` is a module of its own. Its packages may import each other and
  `platform/edge/contract`, the one `platform/` path open to them, and nothing else in
  the repo — never a vendor SDK, the CLI, the SDK or the console.
- **`proto/`** — source of truth for the wire format. Bindings are **generated** — never
  hand-edit generated output.
- **`scripts/`** — development and release tooling, and the emulator and ladder scripts.
- **`www/`** — the docs site at ocel.dev, and what it serves alongside the docs: the
  generated JSON Schema under `public/schema/`.
- **`tests/`** — the suites that drive the real binary — the journeys, the dev-server
  suite and the Next compatibility harness — and under `tests/fixtures/<concern>/` the
  apps they drive. A fixture directory exercises one concern and nothing else. Under
  `tests/fronts/<name>/`, a proxy a vps box runs in front of ocel: the steps that set
  it up before bootstrap, check it after, and take it down, and the `proxy` option its
  projects set. Both the live suite and a journey lane drive them.
- **`docs/agents/`** — configuration the agent skills read. Not product documentation;
  nothing that explains the code belongs here.
- **`.github/`** — CI. **`.changes/`** — the release mechanism; the workflow runs the
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
