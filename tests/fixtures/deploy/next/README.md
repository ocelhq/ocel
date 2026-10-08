# next

A Next.js App Router app that declares no resources at all, so an e2e cell can ask whether
Next runs on a target at all. The surfaces that record their state in postgres live in the
sdk fixture beside it.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
```

`ocel destroy` takes it all down again.

`src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites under
[`tests/e2e`](../../../e2e), and of no use to the product.
