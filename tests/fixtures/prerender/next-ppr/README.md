# next-ppr

A Next.js App Router app with `cacheComponents` on. `/ppr` has a static shell holding one cached
component and one dynamic hole; `/ppr/[id]` is the same with a param, one value of which is
prerendered, so any other value is answered with the route's fallback shell. That page also
reads its param outside the dynamic hole, so a param answered with another's shell shows it.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
```

`ocel destroy` takes it all down again.
