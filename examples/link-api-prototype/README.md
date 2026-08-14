# Link API prototype

Throwaway type-level sketch for [ocelhq/ocel#288](https://github.com/ocelhq/ocel/issues/288):
the publisher-side Link API a user calls inside their SST or Pulumi program — generic
constructor with a token-keyed type registry, versus dedicated per-resource methods.
Never merges; findings and the decision live on the ticket.

## Run

```
node_modules/.bin/tsc -p examples/link-api-prototype
```

A clean pass is the harness: every claimed error is pinned by `@ts-expect-error`
(which itself errors if the line stops failing), and every claimed hole compiles.

## Layout

- `record.ts` — the wire truth both variants produce: name, type token, flat
  property bag, grants (per the link-interface decision on #281).
- `registry.ts` — `LinkProperties`, the token→property-shape registry shipped
  statically in the package. Only `ocel:postgres` today, mirroring reality.
- `stubs/` — just enough `Output`/`Input`, SST, and pulumi-aws surface to make the
  scenarios hover like the real thing, with zero installs.
- `variant-a-generic/` — `new Link(name, { type, properties, grants })`; `type`
  narrows `properties` when the token is in the registry, free-form otherwise.
  `augmentation.ts` is the file a typegen pass would emit for a custom token —
  written by hand, because nothing exists to generate it from (see #288).
  `link(name, resource, fn)` is the ticket's callback form.
- `variant-b-dedicated/` — `link.postgres(name, properties)` plus `link.custom`
  as the escape hatch; `from.ts` holds per-IaC adapters (`fromSstPostgres`,
  `fromRdsInstance`).
- `probes.ts` in each variant — the sharp edges, one named export or call per claim.

## Probes

| Probe | Claim |
| --- | --- |
| A `missingPasswordRejected` | known token: missing property is a compile error |
| A `propertyTypoRejected` | known token: excess property check catches name typos |
| A `typoTokenSilentlyDegrades` | `"ocel:postgress"` falls through to free-form and **compiles** — property typos and all |
| A `numberPropertyRejected` | free-form still enforces `Input<string>` values |
| B `link.postgress` | token typo is an unresolved symbol, not a silent degrade |
| B missing/typo'd property | same compile errors as A's known-token path |

## Scenarios

Both variants run forcing scenario B from the map (#280): an SST VPC + Postgres
published as `main-db`, an SST Bucket published with scoped grants, and the same
pair in raw Pulumi — where `rds.Instance` does not expose a usable password, so
the Pulumi path routes it explicitly (`secret(dbPassword)` / the adapter's `auth`
argument).
