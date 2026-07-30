// Bundles the Node platform artifacts the Go CLI embeds. Driven by generate.sh,
// which owns the input-hash gate and the worker bundles.
import { build } from "esbuild";
import { copyFile, mkdir, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cliDir = dirname(fileURLToPath(import.meta.url));
const root = dirname(cliDir);
const dist = join(cliDir, "dist");

await rm(dist, { recursive: true, force: true });
await mkdir(join(dist, "workers"), { recursive: true });

// cjs is required: the bundled `typescript` is cjs and an esm output breaks on
// its `__filename` references. The externals and loaders are the mock-only and
// asset paths inside @mapbox/node-pre-gyp, reached through @vercel/nft.
await build({
  entryPoints: [join(root, "packages/ocel/src/builder/cli.ts")],
  outfile: join(dist, "builder/cli.cjs"),
  bundle: true,
  platform: "node",
  format: "cjs",
  minify: false,
  external: ["nock", "mock-aws-s3", "aws-sdk"],
  loader: { ".html": "text", ".node": "file" },
});

await build({
  entryPoints: [join(root, "packages/next-runtime/src/next-adapter.mts")],
  outfile: join(dist, "next-runtime/next-adapter.mjs"),
  bundle: true,
  platform: "node",
  format: "esm",
});

// Stays unbundled: the adapter copies it into the user's app, where its bare
// `require("next/dist/...")` must bind to that app's own Next.
await copyFile(
  join(root, "packages/next-runtime/src/edge-cache-handler.cjs"),
  join(dist, "next-runtime/edge-cache-handler.cjs"),
);
