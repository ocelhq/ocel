# express

An Express app the way a team would write one: todos in a Postgres database,
documents in a blob bucket, a health route, and static files. The resources are
declared in `ocel/`, in app code, so there is nothing else to wire up.

`src/probes/` is the test surface: routes the test suite hits that a real app
would not ship. One line in `src/server.ts` mounts it.

## Run it

```sh
pnpm install
ocel dev
```

Deploy it to a target of your own:

```sh
ocel deploy --config ocel.aws.config.ts
ocel deploy --config ocel.vps.config.ts
```

Tear it down with `ocel destroy --config <the same file>`.

The test suite drives this same app against every target. See [tests](../../tests).
