# next-ppr

A Next.js App Router app with `cacheComponents` on, which a Next app has everywhere or nowhere,
so it cannot share [next](../next)'s segment config. It declares nothing.

`/ppr` has a static shell holding one cached component and one dynamic hole; `/ppr/[id]` is the
same with a param, one value of which is prerendered, so any other value is answered with the
route's fallback shell. That page also reads its param outside the dynamic hole, so a param
answered with another's shell shows it.

The rest asks where a read of a prerender comes from. `/cold/1` to `/cold/3` are static pages and
`/cold-ppr/[n]` partially prerendered ones, each read once and by nothing else, so their first
read in a data centre can only come from the durable store or the function. `/warm` is a
partially prerendered page read until its shell is cached, then read again to see the shell stay
put while its dynamic hole moves.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
```

`ocel projects destroy` takes it all down again.
