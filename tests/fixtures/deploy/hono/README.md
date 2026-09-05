# hono

A Hono app served on Node that declares no resources at all. It reads `GREETING` and
`SECRET_TOKEN` straight off `process.env`, so `ocel deploy` has nothing to provision and
the journey suites can ask whether the framework runs on a target at all.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.

`src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites under
[`tests/journeys`](../../../journeys), and of no use to the product.
