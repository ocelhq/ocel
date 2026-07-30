// Bundles the Node platform artifacts the Go CLI embeds. Driven by generate.sh,
// which owns the input-hash gate and the worker bundles.
import { build } from "esbuild";
import { copyFile, mkdir, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const platformDir = dirname(fileURLToPath(import.meta.url));
const root = dirname(dirname(platformDir));
const dist = join(platformDir, "dist");

await rm(dist, { recursive: true, force: true });
await mkdir(join(dist, "workers"), { recursive: true });

// cjs is required: the bundled `typescript` is cjs and an esm output breaks on
// its `__filename` references. The externals and loaders are the mock-only and
// asset paths inside @mapbox/node-pre-gyp, reached through @vercel/nft.
await build({
  entryPoints: [join(platformDir, "src/builder/cli.ts")],
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

// Stay unbundled: the adapter copies these into the user's app verbatim —
// edge-cache-handler.cjs so its bare `require("next/dist/...")` binds to that
// app's own Next, next-dispatch.cjs because it is the bundle launcher's runtime
// peer. The adapter resolves both relative to its own URL, so they must sit
// beside the bundled adapter here exactly as they do in the package's dist.
await Promise.all(
  ["edge-cache-handler.cjs", "next-dispatch.cjs"].map((name) =>
    copyFile(
      join(root, "packages/next-runtime/src", name),
      join(dist, "next-runtime", name),
    ),
  ),
);
