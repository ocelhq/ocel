# 2. Request-time deployment resolution via the deployments store

Date: 2026-07-21

## Status

Accepted

## Context

ADR 0001 freezes the generic edge worker so old deployments can be retained and
switched between cheaply. A frozen, per-app worker script can no longer bake in
anything deployment-specific: the routing manifest, function-URL map, tag
namespace, and — critically — the static assets it currently serves through the
per-script **Workers Assets binding**, which is bound to one script version.

We need the frozen worker to serve whichever Deployment is currently active,
switch to another in under ~5s on rollback, and behave sanely before any
deployment exists.

## Decision

Introduce a per-project **deployments store**: a DO worker (root stack) holding a
single instance with `(app, build id) → Deployment record` plus an ordered
promotion history whose active pointer names the live promotion.

- The frozen worker resolves its app's active Deployment from the store via a
  **service binding** on each request — one `activeRecord` call that folds the
  pointer read and record read into a single round trip — then dispatches.
- **The resolved record is cached in-isolate at a short TTL (~5s)**, which is
  exactly the upper bound on rollback propagation. Because records are immutable
  (keyed by build id), a refresh sends the build id the isolate already holds:
  the store echoes it back and omits the record when the build hasn't moved, so
  an unchanged Deployment revalidates without re-transferring its (potentially
  large) record, and only an actual promotion/rollback pays for a new one.
- **Static assets move to R2** under `assets/<project>/<app>/<build id>/…` in the
  ISR cache-store bucket (disjoint from the `isr/` prefix), served via the Cache
  API with immutable headers and content-type inferred from the path. The Cache
  API is a no-op on `*.workers.dev` but functional on the production custom
  domain this feature targets.
- The deploy host writes records and flips the pointer over the DO worker's
  authenticated endpoint (a project-scoped write secret); the worker itself needs
  no secret.
- **Rollback is a pointer flip.** Promotion aliases the just-built Deployments as
  active; rollback re-points to a prior promotion. GC never deletes the active
  Deployment, so a dangling pointer is impossible by construction.
- When no pointer exists (fresh project), the worker serves a baked-in 404
  **deployment-not-found** page; a transient store outage serves the cached
  deployment, and a cold isolate with an unreachable store returns 503.

## Consequences

- The single DO actor is off the hot path thanks to in-isolate TTL caching.
- Serving assets shifts from Cloudflare-managed Workers Assets to worker-proxied
  R2, so the worker owns content-type mapping and cache headers.
- Rollback latency is governed by the record-cache TTL, not a redeploy.
- The record schema reserves space for future deployment-owned edge workers
  (Next edge routes / middleware), so wiring that later needs no migration.

## Amended: records are keyed by deployment identity, not build id (2026-08-02)

The decision above stands unchanged; only the key it calls "build id" has grown
a second half. A vars-only deploy rotates a baked value by re-rendering the
ciphertext over the existing build output, so it has no new build id, yet it
must still mint its own immutable record. **Deployment identity** is therefore
the build id plus an optional fingerprint of the values baked into it, rendered
as `<build id>~<fingerprint>` — or as the bare build id when nothing is baked,
which is byte-for-byte what this ADR originally described. Read every "build
id" above as a deployment identity where it names the record key
(`(app, build id) → Deployment record`, the revalidation token the isolate
echoes, and the store's own `record:<app>/<buildId>` keys and `buildId` wire
field, whose names predate identities).

The storage prefixes keep the **pure build id**:
`assets/<project>/<app>/<build id>/…` and the ISR and edge-bundle prefixes are
exactly what the build produced, and the several Deployments of one build
legitimately share them — which is why a prune reclaims a build's objects only
once no surviving record still names that build. See CONTEXT.md ("Deployment
identity", "Value fingerprint") and ADR 0005.
