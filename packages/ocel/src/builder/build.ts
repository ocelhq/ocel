import { existsSync, readFileSync, statSync } from "node:fs";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { nodeFileTrace } from "@vercel/nft";
import { init as lexerInit, parse as parseImports } from "es-module-lexer";
import ts from "typescript";
import { resolveFramework, type Framework } from "./registry.js";
import type { AppInput, BuildOptions, FunctionSummary } from "./types.js";

const TS_EXT = new Set([".ts", ".tsx", ".mts", ".cts"]);

/**
 * Strip TypeScript types while preserving everything else the source wrote:
 * modern syntax (`?.`, `??`, …) is kept verbatim because we target ESNext — the
 * Lambda runtime (nodejs24.x) supports it natively, so downleveling it to
 * helper functions would only obscure the user's code. Import/export specifiers
 * are left untouched (paths are rewritten separately, per-file, no bundling).
 * Type-only imports are elided (default, non-verbatim) so no dead ESM import
 * can throw a link error at runtime.
 */
function transpileTs(source: string, ext: string): string {
  return ts.transpileModule(source, {
    fileName: `f${ext}`,
    compilerOptions: {
      target: ts.ScriptTarget.ESNext,
      module: ts.ModuleKind.ESNext,
      isolatedModules: true,
      jsx: ext === ".tsx" ? ts.JsxEmit.React : ts.JsxEmit.None,
    },
  }).outputText;
}
// Source extensions a relative specifier may resolve to, tried in this order.
const RESOLVE_EXT = [".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"];

function resolveEntrypoint(input: AppInput, fw: Framework): string {
  if (input.entrypoint) {
    const abs = path.resolve(input.cwd, input.entrypoint);
    if (!existsSync(abs)) {
      throw new Error(`node-builder: entrypoint "${input.entrypoint}" not found in ${input.cwd}`);
    }
    return abs;
  }
  for (const candidate of fw.entrypointCandidates) {
    const abs = path.resolve(input.cwd, candidate);
    if (existsSync(abs)) return abs;
  }
  throw new Error(
    `node-builder: no entrypoint found in ${input.cwd}; tried: ${fw.entrypointCandidates.join(", ")}`,
  );
}

/** JS output path for a source file (TS -> JS, JS unchanged). */
function toOutExt(rel: string): string {
  const ext = path.extname(rel);
  if (ext === ".ts" || ext === ".tsx") return rel.slice(0, -ext.length) + ".js";
  if (ext === ".mts") return rel.slice(0, -ext.length) + ".mjs";
  if (ext === ".cts") return rel.slice(0, -ext.length) + ".cjs";
  return rel;
}

export interface Placement {
  /** Destination path relative to the `.func` dir. */
  dest: string;
  /** For dependency files, the owning package (root dir + name). */
  pkg?: { root: string; name: string };
}

type PkgCache = Map<string, { name: string } | null>;

/**
 * The package that owns a file: the nearest ancestor directory with a
 * `package.json` that declares a `name`. Handles pnpm store paths, scoped
 * names, and workspace packages (whose real files have no `node_modules/`
 * segment) uniformly. Results are cached per directory.
 */
function findPackage(absFile: string, cache: PkgCache): { root: string; name: string } | undefined {
  let dir = path.dirname(absFile);
  while (true) {
    let entry = cache.get(dir);
    if (entry === undefined) {
      entry = null;
      const pj = path.join(dir, "package.json");
      if (existsSync(pj)) {
        try {
          const name: unknown = JSON.parse(readFileSync(pj, "utf8")).name;
          if (typeof name === "string" && name.length > 0) entry = { name };
        } catch {
          /* malformed package.json — keep walking up */
        }
      }
      cache.set(dir, entry);
    }
    if (entry) return { root: dir, name: entry.name };
    const parent = path.dirname(dir);
    if (parent === dir) return undefined;
    dir = parent;
  }
}

function isUserFile(absPath: string, cwd: string): boolean {
  const rel = path.relative(cwd, absPath);
  return !rel.startsWith("..") && !rel.split(path.sep).includes("node_modules");
}

/**
 * Where a traced file lands in the artifact. App files stay at the root
 * (relative to `cwd`); dependency files are placed by package identity under
 * `node_modules/<name>/<path-within-package>`, preserving each package's
 * internal structure so intra-package relative imports still resolve.
 *
 * Known limitation: node_modules is flat and single-version — if two versions
 * of a package are traced they collapse into one. Acceptable for now.
 */
export function placeFile(absPath: string, cwd: string, cache: PkgCache = new Map()): Placement {
  if (isUserFile(absPath, cwd)) {
    return { dest: path.relative(cwd, absPath) };
  }
  const pkg = findPackage(absPath, cache);
  if (pkg) {
    return { dest: path.join("node_modules", pkg.name, path.relative(pkg.root, absPath)), pkg };
  }
  return { dest: path.join("_external", path.basename(absPath)) };
}

function isUserSource(absPath: string): boolean {
  return !absPath.includes(`${path.sep}node_modules${path.sep}`) && TS_EXT.has(path.extname(absPath));
}

/** The emitted extension for a source extension, matching `toOutExt`. */
function emittedExt(sourceExt: string): string {
  return path.extname(toOutExt(`f${sourceExt}`)) || sourceExt;
}

/**
 * Rewrite an extensionless relative specifier to point at the file this build
 * actually emits. TS allows `./x` and `../dir`; raw Node ESM rejects both.
 * Returns the rewritten specifier, or undefined to leave it untouched.
 */
function rewriteSpecifier(spec: string, sourceDir: string): string | undefined {
  if (!spec.startsWith("./") && !spec.startsWith("../")) return undefined;
  if (/\.(js|mjs|cjs)$/.test(spec)) return undefined;

  const resolved = path.resolve(sourceDir, spec);
  for (const ext of RESOLVE_EXT) {
    if (existsSync(resolved + ext)) return spec + emittedExt(ext);
  }
  if (existsSync(resolved) && statSync(resolved).isDirectory()) {
    for (const ext of RESOLVE_EXT) {
      if (existsSync(path.join(resolved, `index${ext}`))) {
        return `${spec.replace(/\/$/, "")}/index${emittedExt(ext)}`;
      }
    }
  }
  return undefined;
}

/**
 * Add extensions to extensionless relative specifiers in transpiled user code,
 * so the un-bundled module tree resolves under raw Node ESM. Bare/package
 * specifiers are left untouched. Applied to the sucrase output, whose
 * specifiers are byte-for-byte the source's.
 */
async function rewriteRelativeImports(code: string, sourceDir: string): Promise<string> {
  await lexerInit;
  let imports: ReturnType<typeof parseImports>[0];
  try {
    [imports] = parseImports(code);
  } catch {
    // Not parseable as an ES module (or exotic syntax) — leave it untouched.
    return code;
  }
  let out = code;
  for (let i = imports.length - 1; i >= 0; i--) {
    const imp = imports[i]!;
    const spec = imp.n;
    if (!spec || out.slice(imp.s, imp.e) !== spec) continue;
    const rewritten = rewriteSpecifier(spec, sourceDir);
    if (rewritten && rewritten !== spec) {
      out = out.slice(0, imp.s) + rewritten + out.slice(imp.e);
    }
  }
  return out;
}

/**
 * nft only emits files under its `base`, so `base` must enclose the app's
 * resolvable `node_modules` — which, in a hoisted monorepo, lives at the repo
 * root. Use the topmost ancestor of `cwd` that contains a `node_modules` dir.
 */
function traceBase(cwd: string): string {
  let base = cwd;
  let dir = cwd;
  while (true) {
    if (existsSync(path.join(dir, "node_modules"))) base = dir;
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return base;
}

/**
 * readFile hook for nft's analysis: nft's parser throws on TS type syntax and
 * then fails to follow that file's imports, silently under-tracing. Feed it
 * type-stripped JS for TS files (ESM preserved) so it parses and follows
 * imports; the resolver still resolves against real files on disk. The EMIT
 * step still transpiles from source separately.
 */
async function traceReadFile(p: string): Promise<Buffer | string | null> {
  let buf: Buffer;
  try {
    buf = await readFile(p);
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "ENOENT" || code === "EISDIR" || code === "ENOTDIR") return null;
    throw err;
  }
  const ext = path.extname(p);
  if (TS_EXT.has(ext)) {
    try {
      return transpileTs(buf.toString("utf8"), ext);
    } catch {
      return buf;
    }
  }
  return buf;
}

async function emitFile(absPath: string, dest: string): Promise<void> {
  // nft lists pnpm's directory symlinks; the real files are traced separately,
  // so skipping directories reconstructs a flat, resolvable node_modules.
  if (statSync(absPath).isDirectory()) return;
  await mkdir(path.dirname(dest), { recursive: true });
  if (isUserSource(absPath)) {
    const source = await readFile(absPath, "utf8");
    // Per-file, no bundling: strip types only, preserving the user's modern
    // syntax and ESM import/export specifiers (paths rewritten separately).
    const code = transpileTs(source, path.extname(absPath));
    const rewritten = await rewriteRelativeImports(code, path.dirname(absPath));
    await writeFile(toOutExt(dest), rewritten);
    return;
  }
  // Copied deps: many published ESM packages (e.g. the ocel SDK's tsc dist) use
  // extensionless relative imports that only resolve under a bundler. Rewrite
  // those too so the un-bundled tree runs under raw Node ESM. CJS files (no
  // static import/export) are left byte-for-byte — extensionless `require` is
  // fine. Resolve against the file's ORIGINAL location; targets are already JS.
  const ext = path.extname(absPath);
  if (ext === ".js" || ext === ".mjs") {
    const source = await readFile(absPath, "utf8");
    const rewritten = await rewriteRelativeImports(source, path.dirname(absPath));
    if (rewritten !== source) {
      await writeFile(dest, rewritten);
      return;
    }
  }
  await copyFile(absPath, dest);
}

export async function buildApp(
  input: AppInput,
  options: BuildOptions,
): Promise<FunctionSummary> {
  const fw = resolveFramework(input.framework);
  const entrypoint = resolveEntrypoint(input, fw);

  const funcRel = path.join("functions", `${input.name}.func`);
  const funcDir = path.join(options.outDir, funcRel);
  await rm(funcDir, { recursive: true, force: true });
  await mkdir(funcDir, { recursive: true });

  const base = traceBase(input.cwd);
  const { fileList } = await nodeFileTrace([entrypoint], { base, readFile: traceReadFile });

  const pkgCache: PkgCache = new Map();
  const depPackages = new Map<string, string>();
  for (const rel of fileList) {
    const abs = path.resolve(base, rel);
    const placement = placeFile(abs, input.cwd, pkgCache);
    if (placement.pkg) depPackages.set(placement.pkg.root, placement.pkg.name);
    await emitFile(abs, path.join(funcDir, placement.dest));
  }

  // A reconstructed package must carry its package.json so its `exports` map and
  // `type` resolve (e.g. `import "ocel/blob/express"`). Copy any not already
  // emitted by the trace.
  for (const [root, name] of depPackages) {
    const dest = path.join(funcDir, "node_modules", name, "package.json");
    const src = path.join(root, "package.json");
    if (!existsSync(dest) && existsSync(src)) {
      await mkdir(path.dirname(dest), { recursive: true });
      await copyFile(src, dest);
    }
  }

  // The handler is the transpiled entrypoint's path within the `.func` (posix).
  // The nodert runtime imports it directly (OCEL_HANDLER=/var/task/<handler>);
  // there is no generated shim.
  const handler = toOutExt(placeFile(entrypoint, input.cwd, pkgCache).dest).split(path.sep).join("/");
  await writeFile(
    path.join(funcDir, "meta.json"),
    `${JSON.stringify({ runtime: fw.runtime, handler, framework: fw.name }, null, 2)}\n`,
  );

  return {
    name: input.name,
    logicalName: input.logicalName,
    runtime: fw.runtime,
    handler,
    artifactPath: funcRel,
    framework: fw.name,
  };
}

export async function buildApps(
  inputs: AppInput[],
  options: BuildOptions,
): Promise<FunctionSummary[]> {
  // Clear the whole output once per run so a .func from an app that's no longer
  // in `inputs` doesn't linger; per-app builds only touch their own dir after.
  await rm(path.join(options.outDir, "functions"), { recursive: true, force: true });
  const summaries: FunctionSummary[] = [];
  for (const input of inputs) {
    summaries.push(await buildApp(input, options));
  }
  return summaries;
}
