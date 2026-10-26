# @ocel/sst

Publish resources your SST app provisions as ocel bindings, so an ocel app can reach
them by name, and the values it makes as ocel variables, so an app reads them as the
variables it declares.

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
| `parent`      | none                 | The resource in the SST app this binding hangs under.        |

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

No app reads a custom binding, so it takes no `grants`: nothing would attach them.
Naming one in `bindings` is refused for the same reason.

## `bind.variables(values, opts?)`

Publishes values your SST app makes straight into ocel's variable store, where an app
reads each as an ordinary declared variable:

```ts
const apiKey = new sst.Secret("ApiKey");
const orders = new sst.aws.Postgres("Orders", { vpc });

bind.variables({
  API_KEY: apiKey.value,
  DATABASE_URL: $interpolate`postgres://${orders.host}:${orders.port}/${orders.database}`,
});
```

The app declares them as it declares any variable:

```ts
import { defineEnv } from "ocel/env";

export const env = defineEnv({
  API_KEY: { class: "secret" },
  DATABASE_URL: { class: "plain" },
});
```

Each key is a variable an app in the project declares, checked as `ocel env set` checks one:
a key nothing declares is refused at `sst deploy`. Text is stored as it is; a number,
boolean, list or object is stored as JSON.

A value given as a secret output — an `sst.Secret`'s `value` above, or `$util.secret(…)` —
is published as a secret, found with `$util.isSecret`, and the app must declare it
`secret`: a `plain` or `sensitive` variable would carry it into the release. A value that
is not a secret output must not be declared `secret`, since the stack's state keeps it
unencrypted; wrap it in `$util.secret(…)` to publish it as one. `ocel deploy` checks the
pairing again, in case the declaration changed after `sst deploy`.

The values belong to the app and stage that published them: `ocel env ls` shows
`urn:pulumi:<stage>::<app>` as their source, and `ocel env set` and `ocel env rm` refuse
them, naming it: change a value in `sst.config.ts`. Each deploy publishes the whole set
again, so a key the call stops listing is removed. Removing the call removes every value it
wrote, unless another call has written that value since, so moving the call under another
parent keeps the values. Values go to the project root, never to a folder.

`opts` is the same as for `bind.postgres`, without a name: one call publishes everything for
one tier, or one preview environment, so make one call per coordinate; a second call for
one is refused.

## Types

There is one function per ocel binding type an app resolves. A resource ocel cannot
type is not bindable by name — publish what a transform needs from it with
`bind.custom` instead.
