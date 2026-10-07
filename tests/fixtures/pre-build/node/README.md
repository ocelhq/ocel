# pre-build/node

An Express 5 app that reads a `notes` table it never creates. `lifecycle.preBuild` runs
`pnpm migrate` on the deploying machine after the database is provisioned and before the app is
built, and the script creates and fills the table through `postgres("main")` over a port forward,
exactly as it would under `ocel run`. A deploy to a fresh environment serves the rows only if the
command ran against the deployed database first.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel run --env production --config ocel.vps.json -- pnpm migrate
```

`ocel destroy` takes the database and its data down with the app.
