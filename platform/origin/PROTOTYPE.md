# PROTOTYPE — throwaway, never merges

**Question.** If providers are not all-or-nothing, does "one origin + per-role backings,
resolved at plan time inside the origin binary" keep the promise that app code never
changes — and does it refuse the right topologies, with the right fix, before anything is
provisioned? And what does a Pulumi-free origin interface look like?

**Run.**

- Resolution model, clickable: open `prototype/demo.html`.
- Go interface + the same resolver over fakes: `cd platform/origin && go run ./prototype/cmd`
- Config shape: `prototype/config/ocel.config.*.ts`

**Layout.**

- `contract/` — what an origin and a backing satisfy (`Origin`, `Backing`, `Role`,
  `Topology`, `Resolve`). Zero deps, like `platform/edge/contract`.
- `kit/` — the deploy sequencing any origin binary would get for free.
- `prototype/` — fakes, a catalog of four origins and five independent backings, the cmd.
