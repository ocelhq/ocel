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
- `shared/selector.ts` — first-class targeting: the `when` selector every rule
  may carry.
- `variant-a-static/` — patches only: `defineTransform` takes rules of plain
  property objects (`targeting.ts` holds the four targeting cases).
- `variant-b-functions/` — SST-style split: a plain object is a patch, a function
  is an override that sees the fully-defaulted args and mutates or returns them.
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

## Targeting: `when` selectors, first class

`defineTransform` takes a rule or an ordered rule list; each rule optionally
carries `when`. Selector fields AND together; a list within a field ORs; `app`
and `name` take globs, `env` matches the environment identity exactly, `envClass`
is the closed `development | preview | production` union (typo-rejected by probe).

- `when: { app: "api" }` — only that app's resources. Functions carry their app on
  the manifest; a linkable resource matches through the usage-attribution edge
  (#282/#284), meaning "an app that uses it" — but patching a *shared* resource
  through an app selector is ambiguous, so a rule whose `app` selector matches a
  resource also used by non-matching apps fails deploy validation rather than
  silently patching shared infrastructure.
- `when: { name: "assets-*" }` — logical-name glob, any resource type.
- `when: { envClass: "production" }` / `when: { env: "staging" }` — deploy-context
  scoping (deletion protection in production, scale-to-zero in previews).

Every predicate is data resolvable Go-side from the manifest and deploy context —
so targeting does **not** require the function form. Rules apply in order (within
a module, then across the `transforms` list), later rules winning per field.

## Evaluation and carriage

The config crossing is `JSON.stringify(defaultExport)` piped from a node child
(`cli/internal/projectconfig/projectconfig.go:344-389`) — functions cannot ride it.

- **Variant A** needs no new channel: the config evaluator additionally imports
  each module named in `transforms`, and the whole spec — patches plus `$link`
  placeholders — serializes into `StaticTransformWire`, carried beside the provider
  options blob. Go resolves placeholders, merges patches onto defaults, provisions.
  The spec is inspectable and diffable before anything touches AWS.
- **Variant B** adds a deploy-time node pass (`EvaluateRequest`/`EvaluateResponse`
  in `shared/wire.ts`): Go computes the defaulted plain-arg subset per resource,
  hands it to node, node imports the transform modules and applies patches and
  functions in order, hands back final surfaces; placeholders survive serialization
  and Go still resolves them. Deploy already spawns node for discovery, so the hop
  is cheap — but the applied result is no longer knowable without executing user
  code against each resource.

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

**Ship variant A; keep variant B as the pre-designed extension.** Every forcing
scenario on the map — default memory, org tags, VPC placement via a link output —
is expressible statically, and first-class `when` selectors keep targeting (by
app, name, env class, exact env) declarative too; functions earn their place only
when the *value* must be computed from the args or from logic selectors cannot
express, which no scenario yet demands. A's authored
surface is a strict subset of B's (`Patch<T>` is one arm of `Transform<T>`), so
adopting A forecloses nothing: the `transforms` key, the keys, the allowlist, and
`output()` all survive verbatim if functions are ever admitted. A static spec is
also the more trustworthy artifact — serializable, diffable, resolvable to a
concrete plan before AWS is touched — which is where Trust > Product lands.
