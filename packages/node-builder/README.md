# @ocel/node-builder

JS build engine. Turns a framework app (currently Express) into a serverless
`.func` artifact: resolve entrypoint → trace (`@vercel/nft`) → transpile per-file
(esbuild, no bundling) → generate a `serverless-http` handler shim → write the
`.func` directory.

All framework-specific knowledge (entrypoint candidates, Lambda runtime, handler
shim) lives in a single registry: `src/registry.ts`.

## `.func` artifact layout

```
<outDir>/functions/<name>.func/
  index.mjs      generated handler shim (exports `handler`)
  meta.json      { runtime, handler, framework }
  <traced tree>  transpiled user sources + traced node_modules, paths preserved
```

The shim patches `http.Server.prototype.listen` to capture the app's server
without binding a port, imports the user entrypoint as a side effect, then wraps
the captured server's request listener with `serverless-http`.

## stdout contract (Go orchestrator)

The runnable CLI (`src/cli.ts`, bundled to `dist/node-builder.mjs`) reads a
request `{ outDir, apps: AppInput[] }` from argv[2] or stdin and writes a single
JSON object to stdout:

```json
{
  "functions": [
    {
      "name": "api",
      "logicalName": "Api",
      "runtime": "nodejs24.x",
      "handler": "index.handler",
      "artifactPath": "functions/api.func",
      "framework": "express"
    }
  ]
}
```

`artifactPath` is the `.func` directory relative to `outDir` (`.ocel/output` in a
real project). `logicalName` is optional and omitted when unset.

## Build

`pnpm --filter @ocel/node-builder build` bundles the builder plus `@vercel/nft`
and `serverless-http` into `dist/node-builder.mjs` (esbuild stays external — it
ships its own platform binary). A later ticket `go:embed`s this bundle into the
CLI.
