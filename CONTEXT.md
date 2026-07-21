# Context — Ocel Domain Glossary

The ubiquitous language for this project. Glossary only — no implementation
details, no specs. See `docs/adr/` for decisions with lasting consequence.

## Deployment & Rollback

- **Deployment** — the immutable set of artifacts produced for one app by a
  single `ocel deploy` (its Lambda functions, static assets, routing manifest,
  and per-deploy metadata). Identified by a build id. Deployments are never
  mutated after creation; a new `ocel deploy` produces new ones rather than
  updating in place. Production only — previews do not produce rollback-able
  deployments.

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
  one per build). A Deployment record is keyed by (app, build id).

- **Promotion** — the project-wide unit one `ocel deploy` produces: a single
  promotion id grouping that deploy's per-app build ids. The deployments store
  keeps an ordered promotion history; the active pointer is a promotion id (the
  store derives each app's active build id from it). Rollback and retention/GC
  both operate in promotions. See also the verb sense of *Promotion* under
  Deployment & Rollback.

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
  app's Deployment records keyed by (app, build id), plus the active-deployment
  pointer map (app → build id). Framework workers read it via a service binding
  and cache the result in-isolate with a TTL, so the single actor is not hit on
  the hot path.

- **Deployment record** — one entry in the deployments store describing a
  single app Deployment: everything the frozen generic worker needs to serve it
  that used to be baked into the per-deploy worker script — the routing
  manifest, the function-URL map, the tag namespace, the R2 asset prefix (the
  full `assets/<project>/<app>/<build id>` key root), and creation metadata.
  Immutable once written (records are keyed by build
  id), so a worker caches a record indefinitely; only the active-deployment
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
