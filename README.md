<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="www/public/ocel-mark-dark.svg">
    <img src="www/public/ocel-mark.svg" alt="ocel" width="72" height="72">
  </picture>
</p>

<h1 align="center">ocel</h1>

<p align="center">
  Deploy apps anywhere.
  <br>
  <a href="https://ocel.dev/docs">Docs</a> ·
  <a href="https://ocel.dev/docs/quick-start">Quick start</a> ·
  <a href="https://github.com/ocelhq/ocel/issues/new/choose">Report a bug</a>
</p>

> [!WARNING]
> Ocel is in early alpha, so expect bugs. If you hit one, [open an issue](https://github.com/ocelhq/ocel/issues/new/choose).

Ocel is a CLI for deploying apps to your AWS, GCP or VPS without writing infra code. 

You install the CLI, tell it where to deploy to, and run `ocel deploy`.

## Install

```bash
curl -fsSL https://ocel.dev/install.sh | sh
# or: brew install --cask ocelhq/tap/ocel
# or: npm install -g @ocel/cli
```

An `ocel.config.ts` / `ocel.{json,yaml}` is how you configure it:

```ts
import { defineConfig } from "ocel/config";
import awsProvider from "ocel/providers/aws";

export default defineConfig({
  slug: "acme",
  provider: awsProvider(),
  domains: { production: "my-app.example.com" }
});
```

Then, in your app:

```bash
ocel deploy
```

With IaC tooling, you write infra code for how to deploy your app (components for lambda, ecs tasks, api-gateway, etc).
Ocel instead pushes an application-first model. The providers hold the logic for deploying to your platform, and the CLI is the engine that drives them. 
The platform you deploy to is just an implementation detail, a setting.

Compute is a setting too. On providers that support both, the same app runs as functions or as
one container:

```diff
-   apps: [{ name: "web", path: ".", compute: "serverless" }],
+   apps: [{ name: "web", path: ".", compute: "container" }],
```

### Ocel SDK

When using the CLI to deploy, your app can also declare the resources it needs within itself,
no separate infra code to maintain. We like to call this **Application defined infrastructure**

```ts
// infra/index.ts
import { postgres } from "ocel/postgres";

export const db = postgres("main");
```

```ts
// src/server.ts
import { db } from "../infra";

// Use directly in your app
const { rows } = await db.query("select * from documents");
```

The SDK ships for TypeScript, Go, Python and Rust, with
[Postgres](https://ocel.dev/docs/sdk/postgres), [buckets](https://ocel.dev/docs/sdk/bucket),
[KV](https://ocel.dev/docs/sdk/kv), [realtime](https://ocel.dev/docs/sdk/realtime),
[topics](https://ocel.dev/docs/sdk/topic) and [tasks](https://ocel.dev/docs/sdk/task).

During development, the CLI provides a local stack to use:

```bash
# connects your app to resources running locally
ocel dev -- next dev
```

### Ocel Console

Ocel is a CLI-first product, but it also comes with an optional console.
The console is a regular app, itself built with Ocel SDK, and deployed with the Ocel CLI. 

When self-hosting with tools like Coolify/Dokploy, the UI is the control plane,
you install it on your box and perform actions from it. With Ocel, the UI is 
just the first app you deploy into your VPS using Ocel CLI itself! 
It’s just an app that can manage and deploy other apps! 

The console repo is also open source [here](https://github.com/ocelhq/console).

### Ocel Cloud

Ocel offers a managed cloud, intended for small-software, apps that are only intended for few users, not scaling to millions.
It is invite-only during the alpha: join the waitlist at [ocel.dev](https://ocel.dev/#cloud).
You can also use ocel cloud for development and hosting previews while keeping production on your own infra. What you get with cloud:

- Managed hosting
- Unique URLs for your app on `.ocel.app`
- Zero-config deployments. `ocel login` and `ocel deploy` instantly.
- Zero-setup required. On AWS/GCP, ocel needs to bootstrap for the first time running.

### Why not ‘X’:

- Coolify/Dokploy allow you self host, but they are UI first products, and exclusively VPS/docker focused. `ocel` supports more platforms, with flexibility of changing compute and is CLI first, with a UI
- SST,Pulumi,Terraform are IaC tools. If all you need is to deploy your app somewhere, you shouldn’t have to write IaC code. Ocel doesn’t try to replace IaC, you can use with Ocel with IaC tools, keeping each where its most needed, Ocel for apps, and IaC for complex infra setups.
- Encore also allows you to define infra as primitives in code, and deploy to AWS/GCP, but deployments are tightly coupled with their cloud offering. Ocel CLI is standalone and doesn’t need anything else to function. Ocel Cloud itself is built and deployed with Ocel on top of the hyperscalers.
- Nitric (and Encore), have their own api frameworks you use, Ocel deploys your own existing apps and frameworks

## Learn more

- [Documentation](https://ocel.dev/docs)
- [How Ocel compares](https://ocel.dev/docs/compare)
- [Using Ocel with coding agents](https://ocel.dev/docs/agents)

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Report security issues as
[SECURITY.md](SECURITY.md) describes, never in a public issue.

## License

[Apache-2.0](LICENSE)
