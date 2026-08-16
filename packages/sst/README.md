# @ocel/sst

Publish resources your SST app provisions as ocel links, so an ocel app can reach
them by name.

## Install

```bash
pnpm add @ocel/sst
```

`ocel` is a peer dependency, and it is resolved from the ocel project — the
directory holding `ocel.config.ts` — not from the SST app.

## Use

`ocel.config.ts` beside `sst.config.ts` in the same package is the supported
layout. Declare the link in `sst.config.ts`:

```ts
export default $config({
  app() {
    return { name: "shop", home: "aws" };
  },
  async run() {
    const { link } = await import("@ocel/sst");
    const vpc = new sst.aws.Vpc("Vpc");
    const orders = new sst.aws.Postgres("Orders", { vpc });

    link.postgres("orders", orders);
  },
});
```

Name it in `ocel.config.ts`:

```ts
export default defineConfig({
  slug: "shop",
  provider: awsProvider(),
  links: ["orders"],
  apps: [{ name: "api", framework: "express", path: "." }],
});
```

Then read it in the app:

```ts
import { postgres } from "ocel/postgres";

export const orders = postgres("orders");
```

`sst deploy` publishes the link; `ocel deploy` hands it to the app.

## `link.postgres(name, resource, opts?)`

`name` is the name the app binds to. `resource` is either an SST component,
whose own link description is passed through, or the postgres fields written by
hand:

```ts
link.postgres("orders", {
  host,
  port,
  database,
  username,
  password,
  grants: [{ actions: ["rds-db:connect"], resources: [dbUserArn] }],
});
```

`opts` says where the link lands:

| Option        | Default              | Meaning                                                     |
| ------------- | -------------------- | ----------------------------------------------------------- |
| `class`       | `"production"`       | The ocel class the link is published to.                     |
| `environment` | none                 | One preview environment; `class: "preview"` only. Left off, the link serves every preview. |
| `project`     | the SST config root  | The directory holding `ocel.config.ts`.                      |

One call is one resource. Remove the call and the published link goes with it.
A name belongs to whoever published it, so two stacks publishing `orders` into
one project is refused rather than silently handing every app bound to that name
another database.

## Types

There is one function per ocel link type. A resource ocel cannot type is not
linkable, so there is no function to call for it.
