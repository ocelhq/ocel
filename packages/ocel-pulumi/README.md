# @ocel/pulumi

Publish resources your Pulumi program provisions as ocel bindings, so an ocel app can
reach them by name.

## Install

```bash
pnpm add @ocel/pulumi
```

`@pulumi/pulumi` and `ocel` are peer dependencies. `ocel` is resolved from the ocel
project — the directory holding `ocel.json` — not from the Pulumi program.

## Use

`ocel.json` beside `Pulumi.yaml` in the same package is the supported layout.
Declare the binding in the Pulumi program:

```ts
import * as aws from "@pulumi/aws";
import { Config } from "@pulumi/pulumi";
import { bind } from "@ocel/pulumi";

const password = new Config().requireSecret("dbPassword");

const orders = new aws.rds.Instance("orders", {
  engine: "postgres",
  password,
  /* … */
});

bind.postgres("orders", {
  host: orders.address,
  port: orders.port,
  database: orders.dbName,
  username: orders.username,
  password,
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

`pulumi up` publishes the binding; `ocel deploy` hands it to the app.

## `bind.postgres(name, resource, opts?)`

`name` is the name the app binds to. `resource` is the postgres fields, each of them
an input this update resolves before the record is published, so a resource's outputs
are handed over as they are:

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

| Option        | Default                     | Meaning                                                                                    |
| ------------- | --------------------------- | ------------------------------------------------------------------------------------------ |
| `class`       | `"production"`              | The ocel class the binding is published to.                                                     |
| `environment` | none                        | One preview environment; `class: "preview"` only. Left off, the binding serves every preview.   |
| `project`     | the program's directory     | The directory holding `ocel.json`.                                                      |
| `parent`      | none                        | The Pulumi resource this binding hangs under.                                                   |

One call is one resource. Remove the call and the published binding goes with it. A name
belongs to whoever published it — the URN of the resource the call creates — so two
stacks publishing `orders` into one project is refused rather than silently handing
every app bound to that name another database.

## `bind.custom(name, { properties }, opts?)`

Publishes values ocel neither types nor delivers, for a transform to read:

```ts
bind.custom("network", {
  properties: {
    subnetIds: vpc.privateSubnetIds,
    securityGroupIds: [securityGroup.id],
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

The properties are inserted verbatim — string, number, boolean, list or object — and
the surface being filled is what rejects a value of the wrong shape. `opts` is the same
as for `bind.postgres`.

No app reads a custom binding, so it takes no `grants`: nothing would attach them. Naming
one in `bindings` is refused for the same reason.

## Types

There is one function per ocel binding type an app resolves. A resource ocel cannot type is
not bindable by name — publish what a transform needs from it with `bind.custom` instead.
