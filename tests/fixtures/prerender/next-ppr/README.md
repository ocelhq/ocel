# next-ppr

A Next.js App Router app with `cacheComponents` on. `/ppr` has a static shell holding one cached
component and one dynamic hole; `/ppr/[id]` is the same with a param, one value of which is
prerendered, so any other value is answered with the route's fallback shell. `/cold` is a static
page nothing else reads, so a cell can ask where a first read of a prerender comes from.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
```

`ocel destroy` takes it all down again.
