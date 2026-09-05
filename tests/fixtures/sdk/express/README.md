# express

A todos-and-documents app on Express 5. It declares a postgres database, a blob uploader
named `document` that takes images and PDFs under `documents/` and writes a row when an
upload completes, a plain `GREETING` and a secret `SECRET_TOKEN`. The declarations sit in
`ocel/`, and each one is the provisioning step.

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

`src/probes.ts` is the test surface — mounted at `/api/probes`, driven by the suites under
[`tests/journeys`](../../../journeys), and of no use to the product.
