# nestjs

A NestJS 11 app on Express that declares no resources at all, so an e2e cell can ask whether a
Nest app runs on a target at all. `GET /health` answers through an injected service, so a build
that loses Nest's decorator metadata fails it.

## Run it

```bash
pnpm install
ocel dev -- pnpm run dev
```

```bash
ocel deploy
```

`ocel destroy` takes it all down again.

`src/probes.ts` is the test surface, mounted at `/api/probes`, driven by the suites under
[`tests/e2e`](../../../e2e), and of no use to the product.
