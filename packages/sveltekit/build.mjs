import { rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = dirname(fileURLToPath(import.meta.url));
const dist = join(pkgDir, "dist");

await rm(dist, { recursive: true, force: true });

const result = await Bun.build({
  entrypoints: [join(pkgDir, "src/index.ts")],
  outdir: dist,
  naming: "index.js",
  target: "node",
  format: "esm",
  external: ["@sveltejs/kit", "@vercel/nft"],
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}
