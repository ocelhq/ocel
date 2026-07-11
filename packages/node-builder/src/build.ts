import { existsSync, statSync } from "node:fs";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { nodeFileTrace } from "@vercel/nft";
import { transform } from "esbuild";
import { resolveFramework, type Framework } from "./registry";
import type { AppInput, BuildOptions, FunctionSummary } from "./types";

const TS_EXT = new Set([".ts", ".tsx", ".mts", ".cts"]);

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
    const loader = path.extname(absPath) === ".tsx" ? "tsx" : "ts";
    const { code } = await transform(source, {
      loader,
      format: "esm",
      target: "node20",
      sourcefile: path.basename(absPath),
    });
    await writeFile(toOutExt(dest), code);
  } else {
    await copyFile(absPath, dest);
  }
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
  const summaries: FunctionSummary[] = [];
  for (const input of inputs) {
    summaries.push(await buildApp(input, options));
  }
  return summaries;
}
