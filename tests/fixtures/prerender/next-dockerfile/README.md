# next-dockerfile

The `prerender/next` app built from the Dockerfile beside it rather than with railpack. Its
`next build` reads postgres while it prerenders, so it builds only when the image build mounts
the database's binding as a secret and runs on the host network, where `ocel deploy` forwards
a port to the deployed postgres. `ARG OCEL_LIVE_HASH` runs the build again when the binding
changes.

## Run it

```bash
ocel deploy
```

The e2e harness deploys it to a box, from a config it writes over `ocel.json`.

`ocel destroy` takes it all down again.
