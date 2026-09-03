# hello-express

An Express app that declares nothing. It serves a page and a health route, and
that is the whole point: a deploy works with no resources, no config beyond a
slug, and nothing imported from the SDK.

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
