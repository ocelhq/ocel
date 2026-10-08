# pre-build/failing

An Express 5 app whose `lifecycle.preBuild` exits 3. The deploy stops before the app is built, with
the command's exit code and its output in the error, and nothing is promoted: the app never serves.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```

```bash
ocel deploy
```

The e2e harness deploys it to a box, from a config it writes over `ocel.json`.
