import { rm } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { stageSharp } from "@framework/next-image-optimizer/stage-sharp";
import { cjsInterop } from "@framework/node-runtime/cjs-interop";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = process.argv[2] ? join(process.cwd(), process.argv[2]) : join(pkgDir, "dist");
const entrypoint = join(dist, "next", "entrypoint.mjs");

const boxCpus = ["x64", "arm64"];

await rm(dist, { recursive: true, force: true });

const result = await Bun.build({
  entrypoints: [join(pkgDir, "src/next/entrypoint.mts")],
  outdir: dirname(entrypoint),
  naming: basename(entrypoint),
  target: "node",
  format: "esm",
  minify: true,
  external: ["sharp"],
  ...cjsInterop,
});
if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

for (const cpu of boxCpus) stageSharp(join(dist, "sharp", cpu), cpu);

process.stdout.write(`${entrypoint}\n`);
