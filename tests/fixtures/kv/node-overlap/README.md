# kv/node-overlap

An Express 5 app whose one kv store, `notes`, declares two entries whose patterns overlap:
`notes/:id` and `:kind/latest` both name the key `notes/latest`. Its build is refused, naming
both declarations, so it never deploys.

## Run it

```bash
pnpm install
ocel dev -- pnpm dev
```
