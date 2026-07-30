# 4. Pack Next routes into the fewest Lambdas, dispatched by entry key

Date: 2026-07-30

## Status

Accepted

## Context

The Next adapter emitted one Lambda per node route. Routes in a Next build share
nearly all of their traced assets — the `node_modules` forest and the chunk set —
so every route's artifact was very nearly a copy of every other's. A
medium-sized app therefore paid, per route, a full artifact upload, a Pulumi
resource, a Function URL, and a cold start with its own private chunk graph;
nothing was shared between routes that were compiled together.

## Decision

**An app's node routes are packed into the fewest Lambda artifacts their assets
fit in.** A bundle's cost is the union of its members' assets, so a second route
usually costs only its delta. Packing is deterministic — members are visited in
entry-key order and bundles are named `bundle-0`, `bundle-1`, … in the order
they are opened — so an unchanged build yields byte-identical output.

Three things open a new bundle: an artifact-size budget, two members mapping one
destination key to different sources (the second gets its own copy), and a
differing **config class**. A member that overflows the budget on its own ships
as-is and lets AWS reject it with its own clear error, rather than being split
into something that cannot work. A dest-key conflict is therefore a packing
decision, not an error; a *duplicate entry key* is the error, since the launcher's
table is keyed by it and the second member would silently serve the first's
module.

**A manifest may never name something the build did not emit.** Every id and entry
key in the routing manifest is looked up rather than constructed: a route whose
entry no emitted bundle carries, and a prerender whose `parentOutputId` names an
output that renders on neither a Lambda nor the edge, both fail the build. The
alternative is a per-request 502 in production that nothing on either side of the
manifest can catch.

**The route is named per-request, not per-function.** A bundle has one Function
URL, so the request carries an **entry key** in the `x-ocel-entry` header naming
which entry to run; the generated launcher hands a dispatcher the entry table and
its own `require`. There is no fallback to a default entry — the Function URL is
IAM-gated to the edge reader, so every legitimate caller names its entry and an
unknown key is a worker/adapter mismatch worth surfacing as a 502.

Because that header now selects *which code the origin runs*, `x-ocel-*` becomes
a control-plane-only namespace: the worker strips the whole prefix from every
inbound request before building any forward, so an entry key is only ever one the
worker authored.

**One entry per bundle is primed at INIT.** Routes are only ever required whole
(a Turbopack chunk evaluated out of order fails with its dependency's factory
unavailable), so the bundle eagerly requires its **primary entry** — the member
tracing the most *bytes* of assets, ties broken by entry key so the choice is
stable — while the handler module is still loading. Bytes and not file count,
because what priming buys is the shared chunk graph and that graph's cost is
bytes; on a real build the two measures disagree. The bytes are the packer's own,
the same sizing the budget rests on, so the election and the size accounting
cannot disagree about what a route weighs. That is the INIT phase, which
runs at full vCPU regardless of the memory setting, so priming the graph its
bundle-mates share costs nothing. It is best-effort: the prime is caught, so a
primary that throws on import does not fail the container's boot — the failure is
memoized and re-surfaced as that key's own 502, and every other route in the
bundle still serves.

**The provider sizes a bundled function differently.** Because each entry a
container has served stays resident (CommonJS modules cannot be unloaded), peak
RSS grows with the routes that reach it rather than holding at one module's
footprint. A `framework: next` function is therefore provisioned at 1769MB — the
exact point Lambda allocates a full vCPU — for the headroom *and* the CPU to
evaluate lazily-required entries.

### Rejected

- **One Lambda per route.** The status quo. Correct and simple, but it
  duplicates a near-identical artifact per route and shares nothing across a
  cold start.
- **One Lambda per app, unconditionally.** Simpler than packing, but it has no
  answer for an app whose traced assets exceed the artifact limit, and none for
  per-route `maxDuration`/`preferredRegion` once those are honoured.
- **Deriving the entry from the request path inside the Lambda.** It would
  duplicate the edge's routing (rewrites, i18n, interception) in a second place
  and in a second language; the edge has already resolved the route by the time
  it forwards.

## Consequences

- Non-primary entries pay their `require` on the first request that names them —
  in the billed INVOKE phase, where a fractional core would land directly in
  request latency. This is what the full-vCPU memory setting buys back.
- The manifest gains a level of indirection: a pathname resolves to a bundle id
  *and* an entry key, and a prerender's id is the bundle carrying its parent
  route. A prerender parented by an edge route carries an `edgeEntryKey` as well,
  and **the presence of that key alone selects the edge path** — never a failed
  `functionUrls` lookup, which would send an edge render at a Lambda the moment a
  bundle id and a route id collided. Its `id` is consequently optional to the
  worker and read by nothing on that path; the adapter still fills it with the
  parent output's own name for legibility.
- The header is omitted rather than sent empty for a manifest built before
  bundling, whose per-route launcher ignores it.
- **Config class is a stub.** It is constant, so it currently partitions
  nothing: per-route `maxDuration`/`preferredRegion` are not yet honoured
  (bd `ocelhq-kay2`). The packer needs no change when they arrive — they simply
  become the key.
- "Bundle" now names two unrelated artifacts: this one and the pre-existing edge
  bundle (see ADR 0002), which has its own separate entry-key namespace. Both
  are defined in `CONTEXT.md`; prose that touches both tiers has to qualify the
  word.
