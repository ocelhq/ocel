# Provider transform prototype — #291

Type-level sketch of the AWS transform surface (`@ocel/provider-aws/transform`,
naming and positioning locked in #290). Never merges; findings live on the ticket.

Verify: `node_modules/.bin/tsc -p examples/transform-prototype` from the repo root
(zero errors expected; every negative claim is a `@ts-expect-error` probe).

## Layout

- `shared/args.ts` — the transformable surface: per-resource-type keys naming each
  underlying resource, an **allowlist** of plain-value fields per key, and
  `output()` / `LinkOutput` for link-crossing values.
- `shared/wire.ts` — what crosses to Go in each variant.
- `shared/gate.ts` — first-class gating: the `if` predicate every rule may carry,
  over ambient context only.
- `variant-a-static/` — per-resource transforms are patches only: plain property
  objects (`targeting.ts` holds the gate-expressible targeting cases).
- `variant-b-functions/` — SST-style split: a plain object is a patch, a function
  is an override that sees the fully-defaulted args and mutates or returns them
  (`targeting.ts` adds the name-matching case only functions can express).
- `config-sketch.ts` — `transforms` as an `awsProvider(...)` argument.
- `probes.ts` in each variant — one named export per claim.

## The keys (ground truth, not the ticket's guess)

Keys mirror what the provider actually creates today, adjusted for decisions
already locked on the map:

- `function`: `role`, `lambda`, `url`. **There is no `function.logGroup`** — the
  provider never creates log groups (Lambda does, implicitly), so the ticket's
  assumed key does not exist and the probes reject it.
- `bucket`: `bucket`, `cors`, `listener`, `notification` (plus
  `publicAccessBlock`, omitted: nothing on it is safely tunable). The runtime
  role is gone per the membrane decision (#283), so it gets no key.
- `postgres`: `securityGroup`, `subnetGroup`, `cluster`, `instance`.
- Top-level `tags`: unioned into every taggable resource. Scenario C's "org tags
  on all resources" is one line; without this key it is eleven. `ocel:*` prefixes
  are reserved — a transform tag colliding with `resourceTags` output fails deploy
  (runtime check, not typeable).

## Allowlist, not raw passthrough

Each key exposes a typed subset of the Pulumi args, not the full args object.
Raw passthrough was rejected because the Go side builds args from its own structs —
a generic JSON-onto-args merge is exactly the kind of silent-drift surface the
conformance-test decision (#281) exists to avoid, and several inputs are Pulumi
Outputs (role ARN, S3 key) that cannot cross a JSON boundary at all. The allowlist
is also where deploy-time validation lives: an unknown field is an error naming the
module, the key, and the field — never silently dropped.

`function.lambda.vpc` is new surface (no lambda gets VpcConfig today) — it exists
precisely for scenario C. Side observation for the map: postgres lives in the
default VPC while lambdas run outside any VPC, so today's postgres reachability
deserves a check.

## Link outputs: `output(link, property)` placeholders, resolved in Go

`output("network", "appSecurityGroupId")` returns a branded `LinkOutput` token,
serialized as `{"$link": {...}}`. It is accepted wherever the allowlist says
`Input` (string positions only — a probe rejects it in a number position), and a
single `LinkOutput` may stand for a whole list (`InputList`): link records are flat
string bags (#281), so list-valued properties are comma-joined strings the Go side
splits.

Resolution happens **Go-side at provision time**, where the provider already reads
link records for grants and values. Node never sees link values, evaluation stays
deterministic, and an `output()` naming an unlisted or unpublished link fails
deploy with the same hard error the `links` binding uses (#285).

## Gating: the `if` predicate

`defineTransform` takes a rule or an ordered rule list; each rule optionally
carries `if: (ctx) => boolean`. A declarative selector object was tried and
rejected as too rigid — the gate is an ordinary function. Its context is
**ambient only**: `envClass` (the closed `development | preview | production`
union), `env` (the environment identity), and `app` — never anything
resource-specific, which a probe enforces (`ctx.resourceName` is a type error
inside a gate). `app` is the app a candidate resource belongs to; a resource
shared across apps gates with `app: undefined`, so `ctx.app === "api"` is
safely false for shared infrastructure — no special validation needed.

Resource-level targeting (a bucket whose name matches `assets-*`) is not the
gate's job: it lives in the per-resource function form, which receives
`ctx.resourceName` alongside the ambient fields. This is what finally promotes
the function form from reserve to required.

Rules apply in order (within a module, then across the `transforms` list),
later rules winning per field.

## Evaluation and carriage

The config crossing is `JSON.stringify(defaultExport)` piped from a node child
(`cli/internal/projectconfig/projectconfig.go:344-389`) — functions cannot ride
it, and the `if` gate is a function, so a config-time serialized spec is off the
table for both variants. The one mechanism is a **deploy-time node pass**
(`EvaluateRequest`/`EvaluateResponse` in `shared/wire.ts`): Go computes the
defaulted plain-arg subset per resource plus the ambient context (env class,
env identity, owning app), hands it to node; node imports the modules named in
`transforms`, runs each rule's gate against each candidate's ambient context,
applies surviving patches and functions in order, and hands back final surfaces.
`$link` placeholders survive serialization and Go resolves them at provision
time. Deploy already spawns node for discovery, so the hop is cheap, and the
pass's resolved output is still a concrete, diffable artifact — inspectability
moves from the authored spec to the evaluated result.

## The config key

`transforms: string[]` — an argument to `awsProvider(...)`, which stays a function
exactly as today; the top-level config shape does not change. Transforms reshape
how *this provider* provisions, so the provider call is where they belong, and the
key stays visible in `ocel.config.ts` because the provider options are authored
inline there (typed via `AwsProviderOptions`, no longer an untyped record). The
list is ordered, later modules winning where patches collide — scenario C is
naturally three concerns (defaults, tags, placement). Provider affiliation is
doubly enforced: the key lives on the provider, and the module imports
`@ocel/provider-aws/transform`. Rejected spellings: a top-level `transforms` key
(reshapes `OcelConfig` for a provider-specific concern) and `transform` singular
(forces userland composition).

## Recommendation

**Ship variant B's shape.** The reaction rounds settled it: the gate is a
function by requirement, which already commits deploy to the node evaluate pass,
and name-scoped targeting is expressible only in the per-resource function form —
so restricting per-resource transforms to static patches (variant A) buys nothing
once both are true. Static property objects survive as the common-case sugar
(`Patch<T>` is one arm of `Transform<T>`): scenario C is still three plain
objects; functions appear exactly where a gate can't reach (`ctx.resourceName`)
or a value is computed from the defaulted args.
