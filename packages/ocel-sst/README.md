# @ocel/sst

Publish resources your SST app provisions as ocel bindings, so an ocel app can reach
them by name.

## Install

```bash
pnpm add @ocel/sst
```

`ocel` is a peer dependency, and it is resolved from the ocel project — the
directory holding `ocel.json` — not from the SST app.

## Use

`ocel.json` beside `sst.config.ts` in the same package is the supported
layout. Declare the binding in `sst.config.ts`:

```ts
export default $config({
  app() {
    return { name: "shop", home: "aws" };
  },
  async run() {
    const { bind } = await import("@ocel/sst");
    const vpc = new sst.aws.Vpc("Vpc");
    const orders = new sst.aws.Postgres("Orders", { vpc });

    bind.postgres("orders", orders);
  },
});
```

Name it in `ocel.json`:

```json
{
  "$schema": "https://ocel.dev/schema/0.0.1-alpha.0/ocel.schema.json",
  "slug": "shop",
  "provider": { "name": "aws" },
  "bindings": ["orders"],
  "apps": [{ "name": "api", "path": "." }]
}
```

Then read it in the app:

```ts
import { postgres } from "ocel/postgres";

export const orders = postgres("orders");
```

`sst deploy` publishes the binding; `ocel deploy` hands it to the app.

## `bind.postgres(name, resource, opts?)`

`name` is the name the app binds to. `resource` is either an SST component,
whose own binding description is passed through, or the postgres fields written by
hand:

```ts
bind.postgres("orders", {
  host,
  port,
  database,
  username,
  password,
  grants: [{ actions: ["rds-db:connect"], resources: [dbUserArn] }],
});
```

`opts` says where the binding lands:

| Option        | Default              | Meaning                                                     |
| ------------- | -------------------- | ----------------------------------------------------------- |
| `class`       | `"production"`       | The ocel class the binding is published to.                     |
| `environment` | none                 | One preview environment; `class: "preview"` only. Left off, the binding serves every preview. |
| `project`     | the SST config root  | The directory holding `ocel.json`.                      |

One call is one resource. Remove the call and the published binding goes with it.
A name belongs to whoever published it, so two stacks publishing `orders` into
one project is refused rather than silently handing every app bound to that name
another database.

## `bind.custom(name, { properties }, opts?)`

Publishes values ocel neither types nor delivers, for a transform to read:

```ts
bind.custom("network", {
  properties: {
    subnetIds: vpc.privateSubnets,
    securityGroupIds: [vpc.securityGroup],
  },
});
```

A transform module in the ocel project reads them by name:

```ts
export default defineTransform(({ bindings }) => ({
  function: {
    vpc: {
      subnetIds: bindings.network.subnetIds,
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
}));
```

The properties are inserted verbatim — string, number, boolean, list or object —
and the surface being filled is what rejects a value of the wrong shape. `opts`
is the same as for `bind.postgres`.

No app reads a custom binding, so it takes no `grants`: nothing would attach them.
Naming one in `bindings` is refused for the same reason.

## Types

There is one function per ocel binding type an app resolves. A resource ocel cannot
type is not bindable by name — publish what a transform needs from it with
`bind.custom` instead.
