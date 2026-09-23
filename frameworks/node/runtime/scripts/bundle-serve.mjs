import { mkdir, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = process.argv[2] ? join(process.cwd(), process.argv[2]) : join(pkgDir, "dist");
const outfile = join(dist, "serve.mjs");

const result = await Bun.build({
  entrypoints: [join(pkgDir, "src/serve.mts")],
  outdir: dirname(outfile),
  naming: basename(outfile),
  target: "node",
  format: "esm",
  metafile: true,
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

await mkdir(join(dist, ".bundles"), { recursive: true });
await writeFile(
  join(dist, ".bundles/node-runtime.json"),
  JSON.stringify({ inputs: result.metafile.inputs }),
);

process.stdout.write(`${outfile}\n`);
