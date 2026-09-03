# hello-express

An Express 5 app that declares nothing. There is no `ocel/` directory, no SDK call and no
resource: the config carries a slug and one app, and the server answers `/health` and
serves `ocel.svg`.

It is the proof that deploying an app to your own cloud costs you no configuration.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy --config ocel.aws.config.ts
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.
