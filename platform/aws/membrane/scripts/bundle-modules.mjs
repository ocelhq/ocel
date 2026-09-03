import { build } from "esbuild";
import { readdir, rm } from "node:fs/promises";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = process.argv[2] ? join(process.cwd(), process.argv[2]) : join(pkgDir, "dist");
const distNext = join(dist, "next");

const handlers = ["cache-handler", "use-cache-default", "use-cache-remote"];

const internalModules = [
  "cache-store",
  "isr-writer",
  "object-store",
  "tag-clock",
  "use-cache-entry",
  "use-cache-store",
];

const bundledModules = ["router-host"];

const bundledInternals = ["router-assets", "router-signing"];

const cjsInterop = [
  'import { createRequire as ocelCreateRequire } from "node:module";',
  'import { fileURLToPath as ocelFileURLToPath } from "node:url";',
  'import { dirname as ocelDirname } from "node:path";',
  "const require = ocelCreateRequire(import.meta.url);",
  "const __filename = ocelFileURLToPath(import.meta.url);",
  "const __dirname = ocelDirname(__filename);",
].join("\n");

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

await Promise.all(
  bundledModules.map((name) =>
    build({
      entryPoints: [join(pkgDir, `src/next/${name}.mts`)],
      outfile: join(distNext, `${name}.mjs`),
      bundle: true,
      platform: "node",
      format: "esm",
      target: "node24",
      allowOverwrite: true,
      banner: { js: cjsInterop },
    }),
  ),
);

await Promise.all(
  bundledInternals.map((name) => rm(join(distNext, `${name}.mjs`), { force: true })),
);

const keepRelativeImports = {
  name: "keep-relative-imports",
  setup(api) {
    api.onResolve({ filter: /^\.\.?\// }, (args) =>
      args.importer.startsWith(dist) ? { path: args.path, external: true } : undefined,
    );
  },
};

const bundledOutputs = new Set(bundledModules.map((name) => join(distNext, `${name}.mjs`)));
const loose = (await readdir(dist, { recursive: true, withFileTypes: true }))
  .filter((entry) => entry.isFile() && entry.name.endsWith(".mjs"))
  .map((entry) => join(entry.parentPath, entry.name))
  .filter((file) => !bundledOutputs.has(file));

await Promise.all(
  loose.map((file) =>
    build({
      entryPoints: [file],
      outfile: file,
      bundle: true,
      platform: "node",
      format: "esm",
      target: "node24",
      allowOverwrite: true,
      nodePaths: [join(pkgDir, "node_modules")],
      plugins: [keepRelativeImports],
    }),
  ),
);

process.stdout.write(`${loose.map((file) => relative(dist, file)).sort().join("\n")}\n`);
