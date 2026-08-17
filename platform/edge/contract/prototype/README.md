# PROTOTYPE — throwaway

Stub of the `EdgeStack` contract after the ledger split, to react to.
Nothing here ships; the branch is deleted once ocelhq/ocel#391 resolves.

- `edge/` — the proposed `platform/edge/contract` surface (replaces `Provider`, `RootStack`, `SharedEntry`).
- `cloudflare/`, `native/`, `none/` — implementation *signatures* only, showing what each mode fills in.
- `origin/` — the origin-side registry (pull-by-kind) and the DynamoDB ledger, as `platform/aws` would hold them.

Types that do not change (`DeploymentRecord`, `Promotion`, `HistoryEntry`, `PruneResult`,
`Worker`, `WorkerSource`, `Resolver`, `BootstrapOutput`, `Class`) are imported from the
current contract as `cur` so the diff is only what moves.

Run: `cd platform/edge/contract && go test ./prototype/...`
