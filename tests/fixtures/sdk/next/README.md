# next

A todos-and-documents app on Next.js App Router that declares what the SDK offers a Next app,
each declaration the provisioning step: a postgres database, a bucket named `uploads` whose
`document` uploader takes images and PDFs under `documents/` and writes a row when an upload
completes, and one environment through `ocel/env/next`: a plain `GREETING`, a secret
`SECRET_TOKEN`, a sensitive `BUILD_SENSITIVE` and a secret `BUILD_SECRET` that
`/values/[slug]` reads at import, so `next build` needs both, and a public
`NEXT_PUBLIC_GREETING` with a sensitive `SENSITIVE_TOKEN` that `/` renders on the server and in
the browser. The declarations sit in the default discovery directory.

`next build` reads both resources while it prerenders: `/prerender` and
`/prerender/rows/[n]` from postgres, and `/prerender/bucket` from `uploads`, so the app builds
only when the build is handed their bindings. `prerender.Dockerfile` builds the same app in an
image that mounts the postgres binding as a secret on the host network; a box cell names it
with `image.dockerfile`.

It doubles as the fixture the e2e suite under [`tests/e2e`](../../../e2e) drives through
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
