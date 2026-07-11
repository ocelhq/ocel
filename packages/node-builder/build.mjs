import { build } from "esbuild";

// Pre-bundle the handler shims (adapter + serverless-http) into embeddable
// strings first, so the runtime builder never invokes a bundler.
await import("./scripts/gen-shim.mjs");

// Bundle the builder + its runtime deps (@vercel/nft, sucrase, the pre-bundled
// shims) into one self-contained .mjs so the CLI can `go:embed` it. The bundle
// runs inside a user project with plain `node` — it must carry ZERO external
// runtime deps. esbuild is only a build-time tool (used above), never imported
// at runtime, so it is not in this bundle.
await build({
  entryPoints: ["src/cli.ts"],
  outfile: "dist/node-builder.mjs",
  bundle: true,
  platform: "node",
  target: "node20",
  format: "esm",
  // Optional cloud/native deps @vercel/nft's node-pre-gyp tracer references but
  // never needs on the JS-tracing path; leaving them external keeps the bundle
  // lean without breaking it.
  external: ["aws-sdk", "mock-aws-s3", "nock", "node-gyp", "npm"],
  loader: { ".html": "text", ".cs": "text" },
  banner: {
    js: "import { createRequire as __nbCreateRequire } from 'node:module';\nconst require = __nbCreateRequire(import.meta.url);",
  },
});

console.log("built dist/node-builder.mjs");
