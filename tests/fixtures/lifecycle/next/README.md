# next

The lifecycle fixture, a todos-and-documents app on Next.js App Router. It declares a
postgres database, a bucket named `uploads` whose `document` uploader takes images and
PDFs under `documents/` and writes a row when an upload completes, a plain `GREETING` and
a secret `SECRET_TOKEN`. The declarations sit in the default discovery directory, and each one is
the provisioning step.

`lifecycle.preBuild` runs `pnpm migrate` on the deploying machine after the database is
provisioned and before the app is built, over a port forward, exactly as it would under
`ocel run`. The script creates the tables and fills a `notes` table the app never creates, and
`next build` prerenders `/notes` from it, so a deploy to a fresh environment lists the notes only
if the command ran against the deployed database first. `/api/notes` reads them at request time.

It doubles as the fixture the e2e suite under [`tests/e2e`](../../../e2e) drive through
the real binary, so it also has a test surface of no use to the product.

## Run it

```bash
pnpm install
ocel run -- pnpm migrate
ocel dev
```

```bash
ocel deploy
ocel run --env production -- pnpm migrate
```

`ocel destroy` takes it all down again.
