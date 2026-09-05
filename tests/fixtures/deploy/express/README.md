# express

An Express 5 app that declares no resources at all, so a journey can ask whether the
framework runs on a target at all.

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
