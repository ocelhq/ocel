import { build } from "esbuild";
import { rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const distNext = join(pkgDir, "dist/next");

const handlers = ["cache-handler", "use-cache-default", "use-cache-remote"];

const internalModules = ["cache-store", "tag-clock", "use-cache-entry", "use-cache-store"];

await Promise.all(
  handlers.map((name) =>
    build({
      entryPoints: [join(pkgDir, `src/next/${name}.mts`)],
      outfile: join(distNext, `${name}.cjs`),
      bundle: true,
      platform: "node",
      format: "cjs",
      target: "node24",
      minify: true,
      footer: { js: "module.exports = module.exports.default;" },
    }),
  ),
);

await Promise.all(
  [...handlers, ...internalModules].map((name) =>
    rm(join(distNext, `${name}.mjs`), { force: true }),
  ),
);
