// Bundles the Next cache handlers into self-contained CommonJS files for the
// membrane layer — one per handler, since Next loads each registered handler as
// its own module graph.
//
// The layer ships no node_modules, so the AWS clients have to be inlined rather
// than resolved at runtime — the managed runtime's own SDK copy is not something
// to depend on. The output is CJS because Next loads a handler with
// `import(path).then(m => m.default || m)` and then unwraps `.default` again;
// re-exporting the default as module.exports lands on it either way, whether the
// handler is a class (incremental cache) or a plain object (`use cache`).
//
// tsc also emits these sources unbundled (which is what typechecks them), but
// those copies would reach for @aws-sdk at runtime and must not ship.
import { build } from "esbuild";
import { rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const distNext = join(pkgDir, "dist/next");

const handlers = ["cache-handler", "use-cache-default"];

// Sources that only exist to be bundled into a handler; their unbundled tsc
// output must not ship next to the bundles.
const internalModules = ["cache-store", "tag-clock", "use-cache-store"];

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
