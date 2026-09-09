# ocel

Platform as a Framework. `ocel` is the **runtime SDK** your app imports and the types your
config is written against. Declaring a resource in app code — `postgres("main")` — *is* the
provisioning step.

## Install

```sh
npm install ocel
```

It belongs in `dependencies`, not `devDependencies`: the SDK subpaths are imported by your
running app.

## CLI

The `ocel` command is a separate install:

```sh
npm install -g @ocel/cli
curl -fsSL https://ocel.dev/install.sh | sh
brew install ocelhq/tap/ocel
```

## SDK

Every entry point is a subpath — there is no root export.

| Import | Contents |
| --- | --- |
| `ocel/config` | `defineConfig`, and the `OcelConfig` / `AppConfig` / `DomainConfig` / `ProviderDescriptor` types |
| `ocel/postgres` | `postgres(id, config?)` — declares a Postgres database and returns a connected client |
| `ocel/blob` | `bucket`, `uploader`, `createRouteHandler`, `resolveBucketContext` — framework-agnostic |
| `ocel/blob/next` | `bucket`, `uploader`, `createRouteHandler` returning Next route handlers |
| `ocel/blob/hono` | `bucket`, `uploader`, `createRouteHandler` returning a Hono handler |
| `ocel/blob/express` | `bucket`, `uploader`, `createRouteHandler` returning Express middleware |
| `ocel/blob/client` | `createUploadClient` — browser-side uploads against a bucket's uploaders |

`next`, `hono`, `express`, and `pg` are optional peer dependencies; install only the one
your app uses.

```ts
// ocel.config.ts — written by `ocel init`
import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "my-app",
  provider: awsProvider(),
});
```

Point any command at a different config with `--config <path>` (or `OCEL_CONFIG`).

```ts
// ocel/index.ts
import { bucket, uploader } from "ocel/blob/next";
import { postgres } from "ocel/postgres";

export const db = postgres("main");

export const uploads = bucket("uploads", {
  uploaders: {
    // The first argument authorizes the upload and returns the metadata the
    // rest of the uploader — paths, limits, onUploadComplete — receives.
    avatar: uploader(
      { middleware: ({ req }) => ({ userId: req.headers.get("x-user-id") }) },
      { accept: ["image/*"], limits: { maxFileCount: 1 } },
    ),
  },
});
```

Resource declarations resolve through `ocel dev`, so importing them outside a `dev`/`run`
session (or a deployed environment) throws unless the resource's environment variable is
set.
