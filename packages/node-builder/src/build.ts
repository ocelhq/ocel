import { existsSync, statSync } from "node:fs";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { nodeFileTrace } from "@vercel/nft";
import { init as lexerInit, parse as parseImports } from "es-module-lexer";
import { transform } from "sucrase";
import { resolveFramework, type Framework } from "./registry";
import type { AppInput, BuildOptions, FunctionSummary } from "./types";

const TS_EXT = new Set([".ts", ".tsx", ".mts", ".cts"]);
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

/**
 * Destination path (relative to the .func dir) for a traced file. Files under
 * the app root keep their relative path; files reached outside it (e.g. a pnpm
 * store) are flattened back under a single `node_modules/`.
 */
function destFor(absPath: string, base: string): string {
  const rel = path.relative(base, absPath);
  if (!rel.startsWith("..")) return rel;
  const marker = `node_modules${path.sep}`;
  const idx = absPath.lastIndexOf(marker);
  if (idx >= 0) return path.join("node_modules", absPath.slice(idx + marker.length));
  return path.join("_external", path.basename(absPath));
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

async function emitFile(absPath: string, dest: string): Promise<void> {
  // nft lists pnpm's directory symlinks; the real files are traced separately,
  // so skipping directories reconstructs a flat, resolvable node_modules.
  if (statSync(absPath).isDirectory()) return;
  await mkdir(path.dirname(dest), { recursive: true });
  if (isUserSource(absPath)) {
    const source = await readFile(absPath, "utf8");
    // Per-file, no bundling: strip types, preserve ESM import/export specifiers
    // (sucrase does not rewrite import paths). Pure JS, so it bundles into the
    // embedded builder with no native binary.
    const transforms: ("typescript" | "jsx")[] =
      path.extname(absPath) === ".tsx" ? ["typescript", "jsx"] : ["typescript"];
    const { code } = transform(source, { transforms });
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
  const { fileList } = await nodeFileTrace([entrypoint], { base });
  for (const rel of fileList) {
    const abs = path.resolve(base, rel);
    await emitFile(abs, path.join(funcDir, destFor(abs, input.cwd)));
  }

  const entryDest = toOutExt(destFor(entrypoint, input.cwd));
  await writeFile(path.join(funcDir, "index.mjs"), fw.shim(entryDest.split(path.sep).join("/")));
  await writeFile(
    path.join(funcDir, "meta.json"),
    `${JSON.stringify({ runtime: fw.runtime, handler: "index.handler", framework: fw.name }, null, 2)}\n`,
  );

  return {
    name: input.name,
    logicalName: input.logicalName,
    runtime: fw.runtime,
    handler: "index.handler",
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
