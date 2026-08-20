# PROTOTYPE — throwaway, never merges

**Question.** What is the Go framework a new Ocel provider fills in, so that the
lifecycle every provider shares (phases, stages, records, edge, ledger, rollback,
domains, teardown) is written once — without mandating a provisioning engine?

**Run.**

- `cd platform/provider && go run ./prototype/cmd` — an AWS-shaped provider (Pulumi
  adapter) and a VPS-shaped one (ssh+compose, no engine) driven through the same kit.
- `cd platform/provider && go test ./prototype/` — both pass the conformance suite.

**Layout.**

- `contract/` — the ports a provider implements: `Provider` (Facts + seven ports),
  `Deployer` (Plan/Upload/Apply/Open), `Substrate`, `RecordStore`, `VarStore`,
  `Certificates` (optional, by assertion), edge/dns registries.
- `contract/providerconformance/` — what every provider must prove.
- `kit/` — the server over the ports: one stage vocabulary, one phase order, one
  record shape. `kit/pulumi` is a `Deployer` adapter for providers that want Pulumi.
- `prototype/` — fakes, the two shaped providers, the runnable cmd.

The first iteration of this branch (commit e8b5ec24) explored per-role backings across
providers; superseded by this one.
