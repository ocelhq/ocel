<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="www/public/ocel-mark-dark.svg">
    <img src="www/public/ocel-mark.svg" alt="ocel" width="72" height="72">
  </picture>
</p>

<h1 align="center">ocel</h1>

<p align="center">
  Deploy apps to your own cloud or servers with one command.
  <br>
  <a href="https://ocel.dev/docs">Docs</a> ·
  <a href="https://ocel.dev/docs/quick-start">Quick start</a> ·
  <a href="https://ocel.dev/docs/frameworks/nextjs">Next.js</a> ·
  <a href="https://github.com/ocelhq/ocel/issues/new/choose">Report a bug</a>
</p>

> [!WARNING]
> Ocel is in early alpha, so expect bugs. If you hit one, [open an issue](https://github.com/ocelhq/ocel/issues/new/choose).

> [!TIP]
> If Ocel is useful to you, ⭐ **star the repo**: it's the easiest way to follow releases and helps others find it.

## Install

```bash
curl -fsSL https://ocel.dev/install.sh | sh   # or: brew install --cask ocelhq/tap/ocel
                                               # or: npm install -g @ocel/cli
```

Then, in your app:

```bash
ocel deploy
```

The first deploy asks where the app should go, writes the config, and sets up what your account
is missing, asking before each step. See the [quick start](https://ocel.dev/docs/quick-start).

## Zero infrastructure code

You write one small config naming where to deploy, and no infrastructure code. Your app doesn't
know or care where it runs, so moving it is one line:

```diff
  import { defineConfig } from "ocel/config";
- import awsProvider from "ocel/providers/aws";
+ import vpsProvider from "ocel/providers/vps";

  export default defineConfig({
    slug: "acme",
-   provider: awsProvider(),
+   provider: vpsProvider({ ssh: "my-vps" }),
    apps: [{ name: "web", path: "." }],
  });
```

Compute is a setting too. On providers that support both, the same app runs as functions or as
one container:

```diff
-   apps: [{ name: "web", path: ".", compute: "serverless" }],
+   apps: [{ name: "web", path: ".", compute: "container" }],
```

Ocel deploys [Next.js](https://ocel.dev/docs/frameworks/nextjs),
[SvelteKit](https://ocel.dev/docs/frameworks/sveltekit), Node servers such as
[Express](https://ocel.dev/docs/frameworks/express) and [Hono](https://ocel.dev/docs/frameworks/hono),
and Go, Python and Rust apps, to [AWS, Google Cloud or a VPS](https://ocel.dev/docs/providers),
optionally behind [Cloudflare](https://ocel.dev/docs/providers/cloudflare). Infrastructure lands
in your own account, under your billing and access.

## Application defined infrastructure

A database or a bucket is a function call in your app. Declare it once and import it where you
use it; the call is what provisions it.

```ts
// resources/index.ts
import { postgres } from "ocel/postgres";

export const db = postgres("main");
```

```ts
// src/server.ts
import { db } from "../resources";

const { rows } = await db.query("select * from documents");
```

The SDK ships for TypeScript, Go, Python and Rust, with
[Postgres](https://ocel.dev/docs/sdk/postgres), [buckets](https://ocel.dev/docs/sdk/bucket),
[KV](https://ocel.dev/docs/sdk/kv), [realtime](https://ocel.dev/docs/sdk/realtime),
[topics](https://ocel.dev/docs/sdk/topic) and [tasks](https://ocel.dev/docs/sdk/task).

## The stack comes up with dev

```bash
ocel dev -- next dev
```

`ocel dev` starts the resources your code declares on your machine and runs your app with their
connections set. Docker is needed only when the app declares a resource.

## Learn more

- [Documentation](https://ocel.dev/docs)
- [How Ocel compares](https://ocel.dev/docs/compare)
- [Using Ocel with coding agents](https://ocel.dev/docs/agents)

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Report security issues as
[SECURITY.md](SECURITY.md) describes, never in a public issue.

## License

[Apache-2.0](LICENSE)
