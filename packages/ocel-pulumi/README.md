# @ocel/pulumi

Publish resources your Pulumi program provisions as ocel bindings, so an ocel app can
reach them by name.

## Install

```bash
pnpm add @ocel/pulumi
```

`@pulumi/pulumi` and `ocel` are peer dependencies. `ocel` is resolved from the ocel
project — the directory containing `ocel.config.ts` or `ocel.json` — not from the Pulumi program.

## Use

`ocel.config.ts` beside `Pulumi.yaml` in the same package is the supported layout.
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

The grants are published beside the binding, never inside it: a deploy gives them to
the apps bound to `orders`, and an app is handed the postgres fields alone. A grant is
written the way the cloud the app runs in grants:

| Cloud | Grant | Example |
| --- | --- | --- |
| AWS | `{ actions, resources }` | `{ actions: ["rds-db:connect"], resources: [dbUserArn] }` |
| Google Cloud | `{ role, resource }` | `{ role: "roles/cloudsql.client", resource: "projects/my-project" }` |

A grant over `"*"`, a whole service's actions, or every role is refused here, before
anything is published. A deploy refuses a grant its cloud cannot hold: an AWS deploy a
role, and a Google Cloud deploy actions, a basic role (`roles/owner`, `roles/editor`,
`roles/viewer`) or a resource it does not grant on. See
[Grants from a binding](https://ocel.dev/docs/providers/gcp#grants-from-a-binding) for the
resources a Google Cloud grant names.

`opts` says where the binding lands:

| Option        | Default                     | Meaning                                                                                    |
| ------------- | --------------------------- | ------------------------------------------------------------------------------------------ |
| `tier`        | `"production"`              | The ocel tier the binding is published to.                                                     |
| `environment` | none                        | One preview environment; `tier: "preview"` only. Left off, the binding serves every preview that names it under `bindings`.   |
| `project`     | the program's directory     | The directory containing `ocel.config.ts` or `ocel.json`.                                                      |
| `parent`      | none                        | The Pulumi resource this binding hangs under.                                                   |

One call is one resource. Remove the call and the published binding goes with it. A name
belongs to whoever published it — the URN of the resource the call creates — so two
stacks publishing `orders` into one project is refused rather than silently handing
every app bound to that name another database.

## `bind.custom(name, properties, opts?)`

Publishes values ocel neither types nor delivers, for a transform to read:

```ts
bind.custom("network", {
  subnetIds: vpc.privateSubnetIds,
  securityGroupIds: [securityGroup.id],
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

The properties are inserted verbatim — string, number, boolean, list or object — and
the surface being filled is what rejects a value of the wrong shape. `opts` is the same
as for `bind.postgres`, plus `grants`.

No app reads a custom binding's values, and the project's `bindings` refuses one. An app
that needs what the binding grants names it in its own entry instead, and every deploy
gives that app the grants, and none of the values:

```ts
bind.custom(
  "fleet",
  { zone: "us-central1-a" },
  {
    grants: [
      { role: "roles/compute.instanceAdmin.v1", resource: "projects/my-project" },
      {
        role: "roles/iam.serviceAccountUser",
        resource: pulumi.interpolate`projects/my-project/serviceAccounts/${host.email}`,
      },
    ],
  },
);
```

```ts
export default defineConfig({
  slug: "cloud",
  provider: gcpProvider({ project: "my-project", region: "us-central1" }),
  apps: [{ name: "api", path: ".", bindings: { custom: ["@fleet"] } }],
});
```

A custom binding that carries grants may have no properties. A deploy that no longer
binds the app takes the grants back.

## Types

There is one function per ocel binding type an app resolves. A resource ocel cannot type is
not bindable by name — publish what a transform needs from it with `bind.custom` instead.
