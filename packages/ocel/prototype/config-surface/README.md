# PROTOTYPE — throwaway

Stub of the `ocel.config` surface for edge modes, to react to (ocelhq/ocel#399).
Nothing here ships; the branch is deleted once the ticket resolves.

- `ocel/config.ts` — `OcelConfig` with `edge`, `dns`, `allowDegraded`; the `Need` union.
- `ocel/edge.ts` — `cfEdge()`; `ocel/dns.ts` — `cloudflareDns()`.
- `provider-aws/index.ts` — `awsProvider({ region, transforms, certificates })`; `provider-aws/dns.ts` — `route53()`.
- `examples.config.ts` — every mode written out, plus what the type checker refuses.
- `wire.go` — the CLI-side parse + validation and what reaches the origin (`edge_kind`, `dns`, `allow_degraded`).

Run: `cd packages/ocel && npx tsc --noEmit -p prototype/config-surface`
