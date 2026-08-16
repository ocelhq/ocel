# with-transforms

Rung two of the [examples ladder](../README.md). The app is modeled on the
[express](../express) example, with a `postgres` resource; what this example adds is
`ocel.config.ts` and `ocel/transform.ts`. The module is the whole of what it has to show.

## Run it

```sh
pnpm install
ocel deploy
```

To see the effect, look at what landed in your AWS account: the Lambda behind each route,
the Aurora cluster behind `main`, and the tags on both.

`ocel deploy` targets production. Stand a branch environment up with `ocel preview up` to
see the same module render a different result, and `ocel preview rm` to tear it down.
