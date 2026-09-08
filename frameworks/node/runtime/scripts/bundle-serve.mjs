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
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

process.stdout.write(`${outfile}\n`);
