import { createHash } from "node:crypto";
import { existsSync, readFileSync, statSync } from "node:fs";
import { copyFile, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { nodeFileTrace } from "@vercel/nft";
import type { ServeDescriptor } from "@platform/edge-contract/serve";
import { init as lexerInit, parse as parseImports } from "es-module-lexer";
import ts from "typescript";
import {
  NODE_ENTRY_ROUTE_ID,
  SERVE_DESCRIPTOR_FILE,
  appOutDir,
  functionRel,
} from "./layout.js";
import type { AppInput, BuildOptions, FrameworkSpec, FunctionSummary } from "./types.js";

const TS_EXT = new Set([".ts", ".tsx", ".mts", ".cts"]);

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

const RESOLVE_EXT = [".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"];

export function resolveEntrypoint(input: AppInput, spec: FrameworkSpec): string {
  if (input.entrypoint) {
    const abs = path.resolve(input.cwd, input.entrypoint);
    if (!existsSync(abs)) {
      throw new Error(`ocel: entrypoint "${input.entrypoint}" not found in ${input.cwd}`);
    }
    return abs;
  }
  for (const candidate of spec.entrypointCandidates) {
    const abs = path.resolve(input.cwd, candidate);
    if (existsSync(abs)) return abs;
  }
  throw new Error(
    `ocel: no entrypoint found in ${input.cwd}; tried: ${spec.entrypointCandidates.join(", ")}`,
  );
}

function toOutExt(rel: string): string {
  const ext = path.extname(rel);
  if (ext === ".ts" || ext === ".tsx") return rel.slice(0, -ext.length) + ".js";
  if (ext === ".mts") return rel.slice(0, -ext.length) + ".mjs";
  if (ext === ".cts") return rel.slice(0, -ext.length) + ".cjs";
  return rel;
}

export interface Placement {
  dest: string;
  pkg?: { root: string; name: string };
}

type PkgCache = Map<string, { name: string } | null>;

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

function emittedExt(sourceExt: string): string {
  return path.extname(toOutExt(`f${sourceExt}`)) || sourceExt;
}

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

async function rewriteRelativeImports(code: string, sourceDir: string): Promise<string> {
  await lexerInit;
  let imports: ReturnType<typeof parseImports>[0];
  try {
    [imports] = parseImports(code);
  } catch {
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
  if (statSync(absPath).isDirectory()) return;
  await mkdir(path.dirname(dest), { recursive: true });
  if (isUserSource(absPath)) {
    const source = await readFile(absPath, "utf8");
    const code = transpileTs(source, path.extname(absPath));
    const rewritten = await rewriteRelativeImports(code, path.dirname(absPath));
    await writeFile(toOutExt(dest), rewritten);
    return;
  }
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

async function regularFiles(dir: string, prefix = ""): Promise<string[]> {
  const entries = await readdir(dir, { withFileTypes: true });
  const found: string[] = [];
  for (const entry of entries) {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) {
      found.push(...(await regularFiles(path.join(dir, entry.name), rel)));
    } else if (entry.isFile()) {
      found.push(rel);
    }
  }
  return found;
}

export async function artifactHash(dir: string): Promise<string> {
  const rels = (await regularFiles(dir)).sort();
  const outer = createHash("sha256");
  for (const rel of rels) {
    const digest = createHash("sha256").update(await readFile(path.join(dir, rel))).digest("hex");
    outer.update(`${rel}\0${digest}\n`);
  }
  return outer.digest("hex").slice(0, 16);
}

export async function writeServeDescriptor(
  outDir: string,
  app: string,
  descriptor: ServeDescriptor,
): Promise<void> {
  const appDir = appOutDir(outDir, app);
  await mkdir(appDir, { recursive: true });
  await writeFile(
    path.join(appDir, SERVE_DESCRIPTOR_FILE),
    `${JSON.stringify(descriptor, null, 2)}\n`,
  );
}

export async function traceBuild(
  input: AppInput,
  options: BuildOptions,
  spec: FrameworkSpec,
): Promise<FunctionSummary> {
  const entrypoint = resolveEntrypoint(input, spec);

  const funcRel = functionRel(input.name);
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

  for (const [root, name] of depPackages) {
    const dest = path.join(funcDir, "node_modules", name, "package.json");
    const src = path.join(root, "package.json");
    if (!existsSync(dest) && existsSync(src)) {
      await mkdir(path.dirname(dest), { recursive: true });
      await copyFile(src, dest);
    }
  }

  const handler = toOutExt(placeFile(entrypoint, input.cwd, pkgCache).dest).split(path.sep).join("/");
  await writeFile(
    path.join(funcDir, "config.json"),
    `${JSON.stringify({ runtime: spec.runtime, handler, framework: spec.name, id: NODE_ENTRY_ROUTE_ID, app: input.name }, null, 2)}\n`,
  );

  await writeServeDescriptor(options.outDir, input.name, {
    framework: spec.name,
    buildId: await artifactHash(funcDir),
    edgeRouting: false,
    entry: NODE_ENTRY_ROUTE_ID,
    needs: {},
  });

  return {
    name: input.name,
    runtime: spec.runtime,
    handler,
    artifactPath: funcRel,
    framework: spec.name,
    strategy: "trace",
  };
}
