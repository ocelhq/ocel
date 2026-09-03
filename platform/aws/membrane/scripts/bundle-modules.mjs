import { execFileSync } from "node:child_process";
import { isBuiltin } from "node:module";
import { readdir, rm } from "node:fs/promises";
import { basename, dirname, join, relative } from "node:path";
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
execFileSync("tsc", ["--outDir", dist], { cwd: pkgDir, stdio: "inherit" });

await Promise.all(
  handlers.map((name) =>
    bundle(join(pkgDir, `src/next/${name}.mts`), join(distNext, `${name}.cjs`), {
      format: "cjs",
      minify: true,
      footer: "module.exports = module.exports.default;",
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
    bundle(join(pkgDir, `src/next/${name}.mts`), join(distNext, `${name}.mjs`), {
      format: "esm",
      banner: cjsInterop,
    }),
  ),
);

await Promise.all(
  bundledInternals.map((name) => rm(join(distNext, `${name}.mjs`), { force: true })),
);

const looseImports = {
  name: "loose-imports",
  setup(api) {
    api.onResolve({ filter: /^\.\.?\// }, (args) =>
      args.importer.startsWith(dist) ? { path: args.path, external: true } : undefined,
    );
    api.onResolve({ filter: /^[^./]/ }, (args) =>
      args.importer.startsWith(dist) && !isBuiltin(args.path)
        ? { path: Bun.resolveSync(args.path, pkgDir) }
        : undefined,
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
    bundle(file, file, {
      format: "esm",
      plugins: [looseImports],
    }),
  ),
);

process.stdout.write(`${loose.map((file) => relative(dist, file)).sort().join("\n")}\n`);
