# next-dockerfile

The `prerender/next` app built from the Dockerfile beside it rather than with railpack. Its
`next build` reads postgres while it prerenders, so it builds only when the image build mounts
the database's binding as a secret and runs on the host network, where `ocel deploy` forwards
a port to the deployed postgres. `ARG OCEL_LIVE_HASH` runs the build again when the binding
changes.

## Run it

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```

`ocel destroy --config ocel.vps.json` takes it all down again.
