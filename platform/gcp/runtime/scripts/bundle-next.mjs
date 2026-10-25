import { rm } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { stageSharp } from "@framework/next-image-optimizer/stage-sharp";
import { cjsInterop } from "@framework/node-runtime/cjs-interop";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = process.argv[2] ? join(process.cwd(), process.argv[2]) : join(pkgDir, "dist");
const directory = join(dist, "next");

async function bundle(entry, outfile, options) {
  const result = await Bun.build({
    entrypoints: [entry],
    outdir: dirname(outfile),
    naming: basename(outfile),
    target: "node",
    ...options,
  });
  if (!result.success) {
    for (const log of result.logs) console.error(log);
    process.exit(1);
  }
}

await rm(dist, { recursive: true, force: true });

await bundle(join(pkgDir, "src/node/serve.mts"), join(dist, "node", "serve.mjs"), {
  format: "esm",
});

await bundle(join(pkgDir, "src/next/entrypoint.mts"), join(directory, "entrypoint.mjs"), {
  format: "esm",
  minify: true,
  external: ["sharp"],
  ...cjsInterop,
});

stageSharp(directory, "x64");

process.stdout.write(`${directory}\n`);
