import { rm } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const dist = process.argv[2] ? join(process.cwd(), process.argv[2]) : join(pkgDir, "dist");
const folder = join(dist, "next");

const handlers = {
  "cache-handler": Bun.resolveSync("@framework/next-runtime/cache-handler", pkgDir),
  "use-cache-default": Bun.resolveSync("@framework/next-runtime/use-cache-default", pkgDir),
  "use-cache-remote": Bun.resolveSync("@framework/next-runtime/use-cache-remote", pkgDir),
};

const cjsInterop = [
  'import { createRequire as ocelCreateRequire } from "node:module";',
  'import { fileURLToPath as ocelFileURLToPath } from "node:url";',
  'import { dirname as ocelDirname } from "node:path";',
  "const require = ocelCreateRequire(import.meta.url);",
  "const ocelFilename = ocelFileURLToPath(import.meta.url);",
  "const ocelDirnameOf = ocelDirname(ocelFilename);",
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

await Promise.all(
  Object.entries(handlers).map(([name, entry]) =>
    bundle(entry, join(folder, `${name}.cjs`), {
      format: "cjs",
      minify: true,
      footer: "module.exports = module.exports.default;",
    }),
  ),
);

await bundle(join(pkgDir, "src/next/entrypoint.mts"), join(folder, "entrypoint.mjs"), {
  format: "esm",
  minify: true,
  banner: cjsInterop,
  define: { __filename: "ocelFilename", __dirname: "ocelDirnameOf" },
});

process.stdout.write(`${folder}\n`);
