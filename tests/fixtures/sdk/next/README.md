# next

A todos-and-documents app on Next.js App Router. It declares a postgres database, a bucket
named `uploads` whose `document` uploader takes images and PDFs under `documents/` and
writes a row when an upload completes, a plain `GREETING` and a secret `SECRET_TOKEN`. The declarations
sit in the default discovery directory, and each one is the provisioning step.

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
```

`ocel destroy` takes it all down again.
