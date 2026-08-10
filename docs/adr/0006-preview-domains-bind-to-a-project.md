# 6. A preview domain binds to a project, served by one entrypoint worker

Date: 2026-08-10

## Status

Accepted

## Context

`domains.preview` was declared per app, in the same shape as `domains.production`
and read by the same code path. Everything downstream followed from that: a
preview app got its own generic worker (`ocel-<slug>-preview-<app>`), and because
several pointers of one app shared that script, each pointer had to hold a worker
route of its own — an exact `<pointer>-<suffix>.<base>/*`. A preview base domain
was therefore assumed to be *shared* across projects, which forced a chain of
compensations: a per-project label suffix reserved inside the 63-character DNS
label so two projects' pointers could not collide; a `RequiredRecord` on the
wildcard, because a record every project resolved through was nobody's to plant
or reclaim; pruning disabled, because a reconcile knew only the pointer it was
deploying and would have swept its siblings; and pointer teardown reaching out to
the edge to delete routes, recorded on the Deployment record so it could find
them again.

The cost was paid in every direction at once. Route creation is a rate-limited
API call on the critical path of every preview deploy; the pointer's DNS label
was shorter than a DNS label; nothing ever detected two projects claiming the
same base domain, which surfaced much later as an unexplained 404; and a project
with two apps had to declare the same wildcard twice.

## Decision

**A preview domain binds to a project.** Declaring `domains.preview` in a
project's config is that project claiming full authority over that wildcard.
A per-app `domains.preview` is a config error naming the project-level field to
use instead; per-app `domains.production` is untouched, because production apps
genuinely are served on hostnames of their own.

**A project gets exactly one preview entrypoint worker**, `ocel-<slug>-preview`,
attached to exactly one route — `*.<base>/*` — created once for the project's
lifetime. Previews are pointer records in the deployments store and nothing at
the edge, so a preview deploy creates no route and a `preview rm` is pure store
work with zero edge calls. Because the wildcard is the project's complete desired
route set, the reconcile prunes anything else on the project's preview workers —
the entrypoint worker and, named by its own worker-name stem, the per-app workers
this replaced, whose pointer-exact routes outrank the wildcard and would shadow
it on those hostnames otherwise. The host computes the stem and the edge matches
names against it, knowing nothing of what its segments mean; teardown enumerates
the same stem so those workers are reclaimed rather than left billable. Because
the base domain is the project's outright, Ocel plants its own proxied
placeholder record behind it exactly as production does.

**The request's host names both halves.** A pointer is served at
`<pointer>.<base>` when the project has one app and `<pointer>--<app>.<base>`
otherwise; the worker recovers the app half by matching the project's own app
names (bound as a var) rather than by splitting, since either half may contain a
single hyphen. Eliding the app is legal only where there is one app to elide —
there is no configured "default app". The worker is consequently not bound to an
app at all: `OCEL_APP` is production-only now, and what a Deployment is served by
travels on the Deployment record.

**Ownership is checked before the build.** `Preflight` carries the declared
hostnames and reports, per hostname, the worker script currently bound to that
exact route pattern, so a deploy onto a wildcard another project holds is refused
locally before anything is built and nothing is stranded. A hostname held by the
deploying project's own worker is not a claim against it.

Because the base domain is no longer shared, the compensations go with it: the
per-project label suffix and its worker var are deleted and a pointer gets the
whole 63-character label back; `RemoveRoute`, `PointerRemoval.Routes` and
`DeploymentRecord.RouteHostnames` are deleted; and the preview reconcile prunes
like production does.

### Rejected

- **Per-app preview domains** (the status quo). It reads as symmetric with
  production but is not: production hostnames are per-app because a production
  app *is* a site, whereas every preview of every app of one project is served
  under one wildcard. Every app in a project had to repeat the same declaration,
  and the model it implied — a script per app, a route per pointer — is what
  forced the label suffix, the shared-record special case and the per-deploy
  route call.
- **Account-wide preview domains.** A base domain owned by the account, with
  projects allotted subdomains under it, is a real concept and a better fit for
  an organization with many small projects — but it needs an ownership record
  outside any one project's config, a way to see and release allocations, and a
  command surface (`ocel domains`) to manage them. Deferred to that command
  rather than smuggled in as config semantics; the project-level claim is the
  primitive it would be built on.
- **A store-side hostname → (app, pointer) index.** It would let the worker
  resolve any host shape without a grammar, at the price of a lookup on the cold
  path and a second thing to keep consistent with the pointer records. The
  grammar costs one worker var and is decidable in the isolate.

## Consequences

Two projects can still declare overlapping wildcards (`*.foo.com` and
`*.preview.foo.com`) and the claim check will not catch it: matching is
exact-pattern, so the overlap stays the late collision it is today until
`ocel domains` gives ownership somewhere better to live.

A preview project with no worker-backed app is *not* refused for want of a
preview domain. The refusal exists so a preview that would be served does not end
up on an unintended hostname; with nothing to serve, the reconcile only seeds the
project's store instance and binds the worker to no hostname at all. The first
deploy that adds a worker-backed app meets the refusal.

The claim, being one worker route, is account-wide and outlives a run that dies
before tearing its project down — which is why the e2e workflow deploys into one
project per run, serializes runs with a concurrency group, and sweeps stranded
projects before it starts.

A Deployment promoted before `framework` existed carries none, and the field is
required: the entrypoint worker reads an absent one as a framework it has no
handler for and answers 501. A deploy heals itself, because every deploy writes
the field — but a rollback to such a promotion serves 501 for as long as it is
the active one. That break is accepted rather than papered over with a lenient
branch that would have to read an absent field as "next" forever, on records the
worker cannot tell apart from a genuinely unserveable one. The 501 body names the
remedy instead, so the deployment that cannot be served says what to do about it.

`ProjectOwnsWorker` recognises a project's own hold by the `ocel-<slug>` prefix,
so a sibling project slugged `<slug>-something` reads as this one, and a slug
long enough to be clamped in a worker name is not recognised at all — which
refuses rather than lets a deploy through.
