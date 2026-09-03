import tailwind from "@tailwindcss/postcss";
import { copyFile, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import postcss from "postcss";

import { runtimeFiles } from "../../frameworks/next/adapter/scripts/runtime-files.mjs";

const platformDir = dirname(fileURLToPath(import.meta.url));
const root = dirname(dirname(platformDir));
const dist = join(platformDir, "dist");
const workers = join(root, "platform/edge/cloudflare/workers");

async function bundle(entry, outfile, options) {
  const result = await Bun.build({
    entrypoints: [entry],
    outdir: dirname(outfile),
    naming: basename(outfile),
    ...options,
  });
  if (!result.success) {
    for (const log of result.logs) console.error(log);
    process.exit(1);
  }
}

await rm(dist, { recursive: true, force: true });
await mkdir(join(dist, "workers"), { recursive: true });

await bundle(join(platformDir, "src/builder/cli.ts"), join(dist, "builder/cli.cjs"), {
  target: "node",
  format: "cjs",
  external: ["nock", "mock-aws-s3", "aws-sdk"],
  loader: { ".html": "text", ".node": "file" },
});

await bundle(
  join(root, "frameworks/next/adapter/src/next-adapter.mts"),
  join(dist, "next-adapter/next-adapter.mjs"),
  { target: "node", format: "esm" },
);

await bundle(join(platformDir, "src/vars-ui/main.tsx"), join(dist, "vars-ui/app.js"), {
  naming: { entry: "app.[ext]" },
  target: "browser",
  format: "esm",
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

await Promise.all([
  ...runtimeFiles.map((name) =>
    copyFile(
      join(root, "frameworks/next/adapter/src", name),
      join(dist, "next-adapter", name),
    ),
  ),
  copyFile(join(workers, "entry/dist/index.js"), join(dist, "workers/entry-cloudflare.js")),
  copyFile(
    join(workers, "deployments-store/dist/index.js"),
    join(dist, "workers/store-cloudflare.js"),
  ),
  copyFile(
    join(workers, "isr-writer/dist/index.js"),
    join(dist, "workers/isr-writer-cloudflare.js"),
  ),
]);

const hasher = new Bun.CryptoHasher("sha256");
const files = (await readdir(dist, { recursive: true, withFileTypes: true }))
  .filter((entry) => entry.isFile())
  .map((entry) => join(entry.parentPath, entry.name))
  .sort();
for (const file of files) {
  hasher.update(`${relative(dist, file)}\n`);
  hasher.update(await readFile(file));
}
await writeFile(join(dist, "STAMP"), hasher.digest("hex"));
