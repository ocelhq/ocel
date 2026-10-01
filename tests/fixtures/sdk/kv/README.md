# kv

An Express 5 app that declares one kv store, `cache`, with `kv()` from `ocel/kv`, and reaches
it through the SDK: a `PING` on its client, and a write to its text entry read back. The
declaration sits in the default discovery directory and is the provisioning step.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy` takes the store, its data and its password down with the app.
