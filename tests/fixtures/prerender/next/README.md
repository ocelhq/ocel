# next

A Next.js App Router app whose pages read postgres while `next build` prerenders them: a
static home page that reads the database it was built against, and a `generateStaticParams`
route whose params come from a query. It builds only when the build is handed the
database's binding, so an e2e cell can ask whether `ocel deploy` forwards a port to the
deployed postgres for the build. No page reads a table, so a fresh database prerenders it.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
```

The e2e harness deploys it to a box, from a config it writes over `ocel.json`.

`ocel destroy` takes it all down again.
