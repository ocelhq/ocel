# @ocel/sveltekit

The SvelteKit adapter that builds an app into what `ocel deploy` ships: one function that
renders every route, the client and prerendered files beside it, and the `hosting.json` that
tells each host which files are content-hashed.

## Install

```bash
pnpm add -D @ocel/sveltekit
```

Name it as the adapter. On SvelteKit 3, in `vite.config.ts`:

```ts
import ocel from "@ocel/sveltekit";
import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [sveltekit({ adapter: ocel() })],
});
```

On SvelteKit 2.31 or later, in `svelte.config.js`:

```js
import ocel from "@ocel/sveltekit";

export default { kit: { adapter: ocel() } };
```

`ocel deploy` finds the app by its `@sveltejs/kit` dependency and runs its `build` script.

## Running it yourself

Outside `ocel build`, `vite build` writes the same output to `build/`, and `node build` serves
it on `PORT` (3000 by default). That is what a container runs, so give the app a
`"start": "node build"` script.

## `ocel(options?)`

| Option | Default | What it does |
| --- | --- | --- |
| `out` | `"build"` | Where `vite build` writes when `ocel build` names no directory. |

## Client values

`ocel/env/client` is not wired for SvelteKit yet: SvelteKit writes the `tsconfig.json` paths
ocel maps it through for other apps, and its browser bundle does not inline `process.env`. Read
values on the server, in `+page.server.ts` or `+server.ts`, and hand the page what it needs.
