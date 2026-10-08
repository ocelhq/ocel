# next-bucket

A Next.js App Router app whose static home page reads a bucket while `next build` prerenders
it: it lists the bucket and asks for a key nothing wrote. A bucket binding carries no key, so
the page builds only when the build is handed the address and session token of a binding
proxy the provider serves, which an e2e cell can ask whether `ocel deploy` does on each vendor.
A fresh bucket holds nothing, so the page needs no object to exist.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
ocel deploy --config ocel.gcp.json
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy` takes it all down again.
