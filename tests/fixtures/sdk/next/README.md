# next

A todos-and-documents app on Next.js App Router. It declares a postgres database, a blob
uploader named `document` that takes images and PDFs under `documents/` and writes a row
when an upload completes, a plain `GREETING` and a secret `SECRET_TOKEN`. The declarations
sit in `ocel/`, and each one is the provisioning step.

It doubles as the fixture the journey suites under [`tests/journeys`](../../../journeys) drive through
the real binary, so it also carries a test surface of no use to the product.

## Run it

```bash
pnpm install
ocel run -- pnpm migrate
ocel dev
```

```bash
ocel deploy
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.
