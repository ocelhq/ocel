# workspace

One project, two apps, no resources: ocel puts one bootstrap and one edge in front of
both.

The apps live under `apps/*` in the repo's own `pnpm-workspace.yaml`; a project of your own
lists them the same way.

## Run it

```bash
pnpm install
ocel dev -- pnpm --dir apps/next start
ocel dev -- pnpm --dir apps/express start
```

```bash
ocel deploy
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.

`apps/*/src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites
under [`tests/journeys`](../../../journeys), and of no use to the product.
