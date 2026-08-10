import { build } from "esbuild";
import { copyFile, mkdir, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const platformDir = dirname(fileURLToPath(import.meta.url));
const root = dirname(dirname(platformDir));
const dist = join(platformDir, "dist");

await rm(dist, { recursive: true, force: true });
await mkdir(join(dist, "workers"), { recursive: true });

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
  entryPoints: [join(root, "frameworks/next/adapter/src/next-adapter.mts")],
  outfile: join(dist, "next-adapter/next-adapter.mjs"),
  bundle: true,
  platform: "node",
  format: "esm",
});

await build({
  entryPoints: [join(platformDir, "src/vars-ui/main.ts")],
  outfile: join(dist, "vars-ui/app.js"),
  bundle: true,
  platform: "browser",
  format: "esm",
  target: "es2022",
  minify: true,
});
await copyFile(
  join(platformDir, "src/vars-ui/index.html"),
  join(dist, "vars-ui/index.html"),
);

await Promise.all(
  ["edge-cache-handler.cjs", "next-dispatch.cjs"].map((name) =>
    copyFile(
      join(root, "frameworks/next/adapter/src", name),
      join(dist, "next-adapter", name),
    ),
  ),
);
