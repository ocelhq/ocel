# Context — Ocel Domain Glossary

The ubiquitous language for this project. Glossary only — no implementation
details, no specs. See `docs/adr/` for decisions with lasting consequence.

## Project identity & the cloud boundary

- **Slug** — the project's sole identity: a DNS label in `ocel.config.ts`.
  Everything project-scoped derives from it (Pulumi stack names, root-stack state
  keys, the deployments-store DO instance), so it is effectively immutable —
  changing it forks a new project with fresh infrastructure. Wherever these
  entries say "per-project" or `<project>`, the key is the slug.

- **Ocel Cloud** — the hosted control plane, layered *above* the CLI and
  poppable. It is required for `ocel dev` (dev sandboxes are a cloud feature) and
  for nothing else: `init`, `bootstrap`, `deploy`, `preview`, `destroy`,
  `rollback`, and `deployments` run offline or on the user's own AWS
  credentials.

- **Link** — the association between a working tree and an Ocel Cloud
  organization/project, held in `.ocel/link.json` (untracked) rather than in
  `ocel.config.ts`, so a clone can be pointed at a different account. Created
  inline by the first `ocel dev`; managed with `ocel link` / `ocel unlink`.

- **Project root** — the nearest ancestor directory containing `ocel.config.ts`
  or `.ocel/`, falling back to cwd. It anchors resource discovery and is the key
  for dev leader election: one dev server per working tree.

## Deployment & Rollback

- **Deployment** — the immutable set of artifacts produced for one app by a
  single `ocel deploy` (its bundles, its edge bundle, static assets, routing
  manifest, and per-deploy metadata). Identified by a deployment identity.
  Deployments are never mutated after creation; a new `ocel deploy` produces new
  ones rather than updating in place. Production only — previews do not produce
  rollback-able deployments.

- **Active-deployment pointer** — the per-app record in the deployments store
  naming which Deployment an app's worker currently serves. A worker reads only
  its own app's pointer. Rollback and promotion are just re-writes of this
  pointer; the underlying Deployments are untouched.

- **Promotion** — making a Deployment the active one by writing the
  active-deployment pointer to it. A successful `ocel deploy` promotes the
  Deployments it just built. Promotion is **project-wide**: one `ocel deploy`
  flips every app's pointer together, atomically.

- **Rollback** — re-pointing the active-deployment pointer(s) at a
  previously-built Deployment. Project-wide by default (flips every app back to
  the prior project promotion). "Instant" (< 5s) because the old Deployments
  are retained and only the pointer moves.

- **Restorable rollback** — (future, container-backed apps) a slower stack where
  old compute is not kept live; only the artifacts needed to *restore* a
  deployment are retained. Out of scope for the serverless work.

- **Build id** — the per-app identity of one app's built artifacts (Next assigns
  one per build). Static assets, ISR entries and the edge bundle are keyed by it,
  because those bytes are exactly what the build produced.

- **Deployment identity** — what tells one Deployment of an app from another: its
  build id plus an optional fingerprint of the values baked into it, so a
  vars-only deploy reusing the same build output still mints a Deployment of its
  own. A Deployment record is keyed by (app, deployment identity), and the
  app-deploy stack is named after it. Renders as the bare build id when nothing
  is baked.

- **Promotion** — the project-wide unit one `ocel deploy` produces: a single
  promotion id grouping that deploy's per-app deployment identities. The
  deployments store keeps an ordered promotion history; the active pointer is a
  promotion id (the store derives each app's active Deployment from it).
  Rollback and retention/GC both operate in promotions. See also the verb sense
  of *Promotion* under Deployment & Rollback.

## Bundles & dispatch

- **Bundle** — one Lambda artifact serving many of an app's routes. An app's
  node routes are packed into the fewest bundles their traced assets fit in, and
  bundles are named `bundle-0`, `bundle-1`, … in the order they are opened; that
  name is the identity the routing manifest and the function-URL map key on.
  Unqualified "bundle" means this one. _Avoid_: slice, group, pack.

- **Edge bundle** — the single artifact per Deployment holding every edge chunk
  (the `runtime: 'edge'` routes and middleware), which the edge worker compiles
  into a dynamic worker. Deliberately *not* a Bundle: there is one per
  Deployment rather than several, it is not a Lambda, and it carries its own
  entry keys. This is the project's one knowing name collision — qualify the
  word whenever both tiers are in view.

- **Entry** — one compiled route module inside a bundle, and the unit a bundle
  loads. Routes compiling to the same module (a page and its `.rsc` variant,
  say) are one entry, so several pathnames can name it.

- **Entry key** — which entry of a bundle to run. It travels per-request as the
  `x-ocel-entry` header, which is what makes a bundle's single Function URL
  sufficient to address one route. Always authored by the worker and never by a
  client: `x-ocel-*` is the control plane's own header namespace, stripped from
  every inbound request. Node entry keys and edge entry keys are separate
  namespaces and are never interchangeable.

- **Primary entry** — the one entry a bundle requires eagerly, as its handler
  module loads, so the work lands in Lambda's INIT phase and primes the chunk
  graph its bundle-mates share. It is the member tracing the most bytes of
  assets (ties broken by entry key), since that graph's cost is bytes and not
  file count. Every other entry loads lazily, on the first request naming it. A
  primary that throws on import does not sink the bundle: the failure surfaces
  as that entry key's own 502 and its bundle-mates still serve.

- **Config class** — the partition key separating routes that cannot share a
  bundle because they disagree on a Lambda-level setting (`maxDuration`,
  `preferredRegion`). Constant today, so it partitions nothing yet (bd
  `ocelhq-kay2`).

## Provisioning stacks

- **Root stack** (a.k.a. root/prod stack) — the per-project, frozen, generic
  infrastructure: the generic app worker(s), their custom domain(s), and the
  deployments-store worker. Created once per project and reconciled only on an
  ocel version upgrade — never mutated by a user `ocel deploy`. Managed
  imperatively via the edge provider's API (not a Pulumi stack).

- **Infra stack** (infra stack) — the per-project Pulumi stack holding SDK-declared
  resources (postgres, bucket, …). Runs before app stacks that depend on its
  outputs. Untouched by rollback.

- **App-deploy stack** (app-deploy stack) — a per-app, per-deploy Pulumi stack
  that produces a Deployment's immutable compute artifacts (Lambda functions).
  Its stack name is unique on every deploy (unlike root/infra, which are
  stable). Parallelizable across apps.

## Deployments store

- **Deployments store** — the deployments-DO worker in the root stack, one per
  project. Holds a single Durable Object instance for the whole project: every
  app's Deployment records keyed by (app, deployment identity), plus the
  active-deployment pointer map (app → deployment identity). Framework workers
  read it via a service binding and cache the result in-isolate with a TTL, so
  the single actor is not hit on the hot path.

- **Deployment record** — one entry in the deployments store describing a
  single app Deployment: everything the frozen generic worker needs to serve it
  that used to be baked into the per-deploy worker script — the routing
  manifest, the function-URL map, the tag namespace, the R2 asset prefix (the
  full `assets/<project>/<app>/<build id>` key root), and creation metadata.
  Immutable once written (records are keyed by deployment
  identity), so a worker caches a record indefinitely; only the active-deployment
  pointer carries a short TTL (~5s), which bounds how long a rollback takes to
  propagate.

- **Asset store** — deployment static assets live in the account-global R2 ISR
  cache-store bucket under a dedicated `assets/<project>/<app>/<build id>/…`
  prefix, disjoint from the `isr/` prefix (immutable/deployment lifecycle vs
  tag+TTL lifecycle). The frozen worker serves them via the Cache API with
  immutable headers and content-type inferred from the path — replacing the old
  per-script Workers Assets binding, which cannot survive a frozen worker.

- **Root-stack version stamp** — a version marker (held in the deployments store)
  recording which ocel root-stack revision is deployed. A deploy reconciles the
  otherwise-frozen root stack (re-puts the generic + DO workers, migrates the DO
  schema) only when the running ocel expects a newer revision; otherwise it
  touches nothing.

- **Project-scoped write secret** — the credential the deploy host presents to
  the DO worker to write records and flip the pointer. Minted when the root stack
  is created, bound as a secret on the DO worker, and persisted in the provider's
  per-project state. The frozen worker needs none — it reads via a service
  binding. Note: this feature is production-only; previews keep the existing
  single-in-place-stack model with no deployments store and no rollback.

- **Deployment-not-found** — what the frozen worker serves (a branded 404 page
  baked into its bundle) when no active-deployment pointer exists for its app
  yet — e.g. a fresh project whose domain resolves before the first promotion.
  Distinct from a transient store outage: a cached deployment keeps serving when
  the store is briefly unreachable; only a cold isolate with an unreachable
  store yields a 503.
