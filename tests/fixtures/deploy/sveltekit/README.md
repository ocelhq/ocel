# sveltekit

A SvelteKit 3 app built through `@ocel/sveltekit` that declares no resources at all, so an e2e
cell can ask whether SvelteKit runs on a target at all: server rendering, a prerendered page,
content-hashed chunks, and a form action behind SvelteKit's origin check.

## Run it

```bash
pnpm install
ocel dev -- pnpm run dev
```

```bash
ocel deploy
```

`ocel destroy` takes it all down again.

`src/probes.ts` is the test surface, mounted at `/api/probes` and driven by the suites under
[`tests/e2e`](../../../e2e). It is of no use to the product.
