# ocel

Platform as a Framework. `ocel` is one package with two faces: the **CLI** that provisions
and deploys, and the **runtime SDK** your app imports. Declaring a resource in app code —
`postgres("main")` — *is* the provisioning step.

## Install

```sh
npm install ocel
```

It belongs in `dependencies`, not `devDependencies`: the SDK subpaths are imported by your
running app. The CLI binary is delivered through platform-specific
`optionalDependencies` (`@ocel/darwin-arm64`, `@ocel/darwin-x64`, `@ocel/linux-x64`,
`@ocel/win32-x64`), so only your platform's is downloaded.

## CLI

```sh
npx ocel init          # write ocel.config.ts and add the provider to dependencies
npx ocel dev -- <cmd>  # run <cmd> with resource connections in its environment
npx ocel deploy        # deploy to the provider configured in ocel.config.ts
```

`init` runs entirely offline. `dev` and `deploy` need an authenticated, linked project.

| Command | Purpose |
| --- | --- |
| `ocel init [slug]` | Make this directory deployable |
| `ocel dev -- <cmd>` | Run your project in development mode |
| `ocel run -- <cmd>` | Run a one-off command with your project's resource connections |
| `ocel build` | Build your project's apps into `.ocel/output` without deploying |
| `ocel deploy` | Deploy your project to its configured cloud provider |
| `ocel preview up\|rm\|ls\|prune` | Manage per-branch preview environments |
| `ocel rollback` | Roll production back to a previous deployment |
| `ocel deployments ls\|prune` | Manage production deployments |
| `ocel destroy` | Permanently destroy this project's entire production deployment |
| `ocel bootstrap` | Provision the account-global resources your provider needs |
| `ocel login` / `ocel logout` | Authenticate the CLI with your account |
| `ocel link [project]` / `ocel unlink` | Link this directory to an Ocel Cloud project |

Run `ocel <command> --help` for flags.

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
import awsProvider from "@ocel/provider-aws";

export default defineConfig({
  slug: "my-app",
  provider: awsProvider(),
});
```

```ts
// ocel/index.ts
import { bucket, uploader } from "ocel/blob/next";
import { postgres } from "ocel/postgres";

export const db = postgres("main");

export const uploads = bucket("uploads", {
  uploaders: {
    avatar: uploader({}, { accept: ["image/*"] }),
  },
});
```

Resource declarations resolve through `ocel dev`, so importing them outside a `dev`/`run`
session (or a deployed environment) throws unless the resource's environment variable is
set.
