import { mkdir, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..");
const dist = join(here, "dist");

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });

const result = await Bun.build({
  entrypoints: [join(root, "packages/ocel-transforms/src/run.ts")],
  outdir: dist,
  naming: "runner.mjs",
  target: "node",
  format: "esm",
  minify: false,
  metafile: true,
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

await mkdir(join(dist, ".bundles"));
await writeFile(
  join(dist, ".bundles/transform-runner.json"),
  JSON.stringify({ inputs: result.metafile.inputs }),
);
