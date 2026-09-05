# workspace

One project, two apps, one set of declarations. `ocel/` at the root declares a postgres
database, a blob uploader named `document`, a plain `GREETING` and a secret `SECRET_TOKEN`
once, and `apps/next` and `apps/express` import from it. Each app mounts the uploader with
its own route handler, and ocel works out from each app's imports which resources it is
handed.

The config names each app, points at it under `apps/`, and gives it its own env folder, and
in production its own hostname. There is one bootstrap and one edge in front of both.
`APP_NAME` is the value that tells each running app which hostname is its own.

The apps live under `apps/*` in the repo's own `pnpm-workspace.yaml`; a project of your own
lists them the same way.

## Run it

```bash
pnpm install
ocel run -- pnpm migrate
APP_NAME=next ocel dev -- pnpm --dir apps/next start
APP_NAME=express ocel dev -- pnpm --dir apps/express start
```

```bash
ocel deploy
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.

`apps/*/src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites
under [`tests/journeys`](../../../journeys), and of no use to the product.
