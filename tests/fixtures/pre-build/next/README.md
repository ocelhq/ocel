# pre-build/next

A Next.js App Router app whose home page `next build` prerenders from a `notes` table the app
never creates. `lifecycle.preBuild` runs `pnpm migrate` on the deploying machine after the
database is provisioned and before the app is built, and the script creates and fills the table
through `postgres("main")` over a port forward. A deploy to a fresh environment builds, and its
prerendered page lists the notes, only if the command ran against the deployed database first.

## Run it

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy --config ocel.vps.json` takes the database and its data down with the app.
