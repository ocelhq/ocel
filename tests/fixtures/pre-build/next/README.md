# pre-build/next

A Next.js App Router app whose home page `next build` prerenders from a `notes` table the app
never creates. `lifecycle.preBuild` runs `pnpm migrate` on the deploying machine after the
database is provisioned and before the app is built, and the script creates and fills the table
through `postgres("main")` over a port forward. A deploy to a fresh environment builds, and its
prerendered page lists the notes, only if the command ran against the deployed database first.

## Run it

```bash
ocel deploy
```

The e2e harness deploys it to a box, from a config it writes over `ocel.json`.

`ocel destroy` takes the database and its data down with the app.
