// Bundles the Next cache handler into a single self-contained CommonJS file for
// the membrane layer.
//
// The layer ships no node_modules, so the AWS clients have to be inlined rather
// than resolved at runtime — the managed runtime's own SDK copy is not something
// to depend on. The output is CJS because Next loads the handler with
// `import(path).then(m => m.default || m)` and then unwraps `.default` again;
// exporting the class as module.exports lands on the class either way.
//
// tsc also emits these sources unbundled (which is what typechecks them), but
// those copies would reach for @aws-sdk at runtime and must not ship.
import { build } from "esbuild";
import { rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const distNext = join(pkgDir, "dist/next");

await build({
  entryPoints: [join(pkgDir, "src/next/cache-handler.mts")],
  outfile: join(distNext, "cache-handler.cjs"),
  bundle: true,
  platform: "node",
  format: "cjs",
  target: "node24",
  minify: true,
  footer: { js: "module.exports = module.exports.default;" },
});

await Promise.all(
  ["cache-handler.mjs", "cache-store.mjs"].map((f) =>
    rm(join(distNext, f), { force: true }),
  ),
);
