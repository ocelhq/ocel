import { build } from "esbuild";

// Bundle the builder + its heavy deps (@vercel/nft, serverless-http) into one
// self-contained .mjs so the CLI can `go:embed` it. esbuild itself stays
// external: it ships its own platform binary and cannot be bundled.
await build({
  entryPoints: ["src/cli.ts"],
  outfile: "dist/node-builder.mjs",
  bundle: true,
  platform: "node",
  target: "node20",
  format: "esm",
  // esbuild ships its own platform binary; the rest are optional cloud/native
  // deps @vercel/nft's node-pre-gyp tracer references but never needs here.
  external: ["esbuild", "aws-sdk", "mock-aws-s3", "nock", "node-gyp", "npm"],
  loader: { ".html": "text", ".cs": "text" },
  banner: {
    js: "import { createRequire as __nbCreateRequire } from 'node:module';\nconst require = __nbCreateRequire(import.meta.url);",
  },
});

console.log("built dist/node-builder.mjs");
