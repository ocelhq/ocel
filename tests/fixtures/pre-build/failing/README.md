# pre-build/failing

An Express 5 app whose `lifecycle.preBuild` exits 3. The deploy stops before the app is built, with
the command's exit code and its output in the error, and nothing is promoted: the app never serves.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
OCEL_VPS_HOST=… OCEL_VPS_USER=… OCEL_VPS_IDENTITY_FILE=… ocel deploy --config ocel.vps.json
```
