# next-store

A Next.js App Router app with `cacheComponents` on, whose pages a cell reads to ask where a read
of a prerender comes from. `/cold/1` to `/cold/3` are static pages and `/cold-ppr/[n]` partially
prerendered ones, each read once and by nothing else, so their first read in a data centre can
only come from the durable store or the function. `/warm` is a partially prerendered page read
until its shell is cached, then read again to see the shell stay put while its dynamic hole
moves.

## Run it

```bash
pnpm install
ocel dev
```

```bash
ocel deploy
```

`ocel destroy` takes it all down again.
