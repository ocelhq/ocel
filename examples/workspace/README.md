# workspace

One project serving three apps. The config declares the `next`, `express` and `hono`
examples by relative path, names each app after its framework, and gives each its own env
folder and its own production hostname. There is one bootstrap and one edge in front of
all three.

The apps' own `ocel/` directories are the project's declarations, so the three share one
postgres and one bucket, and `APP_NAME` is the value that tells each running app which of
the three hostnames belongs to it.

## Run it

```bash
pnpm install
ocel run -- pnpm --dir ../next migrate
APP_NAME=next ocel dev -- pnpm --dir ../next start
APP_NAME=express ocel dev -- pnpm --dir ../express start
APP_NAME=hono ocel dev -- pnpm --dir ../hono start
```

```bash
ocel deploy --config ocel.aws.config.ts
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.config.ts
```

`ocel destroy` takes it all down again.
