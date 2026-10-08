# node

A todos-and-documents app on Node, served by Express 5. It declares a postgres database, a
bucket named `uploads` whose `document` uploader takes images and PDFs under `documents/`
and writes a row when an upload completes, a plain `GREETING` and a secret `SECRET_TOKEN`. The
declarations sit in the default discovery directory, and each one is the provisioning step.

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

`src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites under
[`tests/e2e`](../../../e2e), and of no use to the product.
