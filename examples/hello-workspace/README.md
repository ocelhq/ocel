# hello-workspace

Two apps that declare nothing, deployed as one project. There is no `ocel/` directory, no
SDK call and no resource: the config carries a slug and two apps under `apps/`, a Next.js
App Router app and an Express 5 app, and each answers `/health` with its own name and
serves `ocel.svg`.

It is the proof that deploying a multi-app project to your own cloud costs you no more
configuration than a single app does. The apps live under `apps/*` in the repo's own
`pnpm-workspace.yaml`; a project of your own lists them the same way.

## Run it

```bash
pnpm install
ocel dev -- pnpm --dir apps/next start
ocel dev -- pnpm --dir apps/express start
```

```bash
ocel deploy --config ocel.aws.config.ts
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.
