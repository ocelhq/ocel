# node

A Node app, served by Express 5, that declares no resources at all, so an e2e cell can ask
whether a node runtime runs on a target at all.

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
