# @ocel/sst

Publish resources your SST app provisions as ocel bindings, so an ocel app can reach
them by name.

## Install

```bash
pnpm add @ocel/sst
```

Each binding is published by running the ocel CLI in the ocel project, the directory
containing `ocel.config.ts` or `ocel.json`, so the CLI that deploys the project is the one
that publishes to it. The first of these is run:

1. `$OCEL_CLI`, the path to an `ocel` binary.
2. `ocel` on `PATH`, as `curl -fsSL https://ocel.dev/install.sh | sh` or a CI setup step puts it.
3. The `ocel` bin of `@ocel/cli` resolved from the ocel project, when the project installs it
   with npm.

With none of the three, `sst deploy` fails naming them.

## Use

`ocel.config.ts` beside `sst.config.ts` in the same package is the supported
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

Name it in `ocel.config.ts`:

```ts
import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "shop",
  provider: awsProvider(),
  bindings: { postgres: { orders: "@orders" } },
  apps: [{ name: "api", path: "." }],
});
```

Then read it in the app:

```ts
import { postgres } from "ocel/postgres";

export const orders = postgres("orders");
```

`sst deploy` publishes the binding; `ocel deploy` hands it to the app.

## `bind.postgres(name, resource, opts?)`

`name` is the name the app binds to. `resource` is either an SST component,
whose own link (its properties and the permissions it includes) is passed through,
or the postgres fields written by hand:

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

The grants are published beside the binding, never inside it: a deploy gives them to
the apps bound to `orders`, and an app is handed the postgres fields alone.

`opts` says where the binding lands:

| Option        | Default              | Meaning                                                     |
| ------------- | -------------------- | ----------------------------------------------------------- |
| `tier`        | `"production"`       | The ocel tier the binding is published to.                     |
| `environment` | none                 | One preview environment; `tier: "preview"` only. Left off, the binding serves every preview that names it under `bindings`. |
| `project`     | the SST config root  | The directory containing `ocel.config.ts` or `ocel.json`.                      |

One call is one resource. Remove the call and the published binding goes with it.
A name belongs to whoever published it, so two stacks publishing `orders` into
one project is refused rather than silently handing every app bound to that name
another database.

## `bind.custom(name, properties, opts?)`

Publishes values ocel neither types nor delivers, for a transform to read:

```ts
bind.custom("network", {
  subnetIds: vpc.privateSubnets,
  securityGroupIds: vpc.securityGroups,
});
```

A transform module in the ocel project reads them by name:

```ts
export default defineTransform(({ bindings }) => ({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          subnetIds: bindings.custom.network.subnetIds,
          securityGroupIds: bindings.custom.network.securityGroupIds,
        },
      },
    },
  },
}));
```

The properties are inserted verbatim — string, number, boolean, list or object —
and the surface being filled is what rejects a value of the wrong shape. `opts`
is the same as for `bind.postgres`.

No app reads a custom binding's values, and the project's `bindings` refuses one.
`bind.custom` here publishes no grants; an app receives the grants a record is
published with elsewhere, such as with `@ocel/pulumi`, by naming it in its own
`bindings.custom`.

## Types

There is one function per ocel binding type an app resolves. A resource ocel cannot
type is not bindable by name — publish what a transform needs from it with
`bind.custom` instead.
