import tailwind from "@tailwindcss/postcss";
import { build } from "esbuild";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import postcss from "postcss";

import { runtimeFiles } from "../../frameworks/next/adapter/scripts/runtime-files.mjs";

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
  entryPoints: [join(platformDir, "src/vars-ui/main.tsx")],
  outfile: join(dist, "vars-ui/app.js"),
  bundle: true,
  platform: "browser",
  format: "esm",
  target: "es2022",
  jsx: "automatic",
  tsconfig: join(platformDir, "tsconfig.vars-ui.json"),
  minify: true,
});
const sheet = join(platformDir, "src/vars-ui/styles.css");
const compiled = await postcss([tailwind({ optimize: { minify: true } })]).process(
  await readFile(sheet, "utf8"),
  { from: sheet, to: join(dist, "vars-ui/app.css") },
);
await writeFile(join(dist, "vars-ui/app.css"), compiled.css);
await copyFile(
  join(platformDir, "src/vars-ui/index.html"),
  join(dist, "vars-ui/index.html"),
);

await Promise.all(
  runtimeFiles.map((name) =>
    copyFile(
      join(root, "frameworks/next/adapter/src", name),
      join(dist, "next-adapter", name),
    ),
  ),
);
