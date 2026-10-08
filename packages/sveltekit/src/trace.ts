import { copyFileSync, mkdirSync, realpathSync, rmSync, statSync, symlinkSync } from "node:fs";
import { readlink } from "node:fs/promises";
import { basename, dirname, join, parse, relative, resolve, sep } from "node:path";
import { nodeFileTrace } from "@vercel/nft";

export interface Bundled {
  entryFile: string;
  ancestor: string;
}

export interface TraceLog {
  warn(message: string): void;
}

export async function bundleFunction(
  entry: string,
  functionDir: string,
  log: TraceLog,
): Promise<Bundled> {
  rmSync(functionDir, { force: true, recursive: true });
  const base = parse(resolve(entry)).root;
  const traced = await nodeFileTrace([entry], {
    base,
    processCwd: process.cwd(),
    ignore: (file) => file.startsWith("**"),
    readlink: readlinkFromRealParent,
  });

  const unresolved: string[] = [];
  for (const warning of traced.warnings) {
    if (warning.message.startsWith("Failed to resolve dependency node:")) continue;
    if (warning.message.startsWith("Failed to parse")) continue;
    if (/Failed to resolve dependency "[^"]*\.node"/.test(warning.message)) continue;
    if (warning.message.startsWith("Failed to resolve dependency")) {
      unresolved.push(warning.message);
      continue;
    }
    throw warning;
  }
  if (unresolved.length > 0) {
    log.warn(
      `ocel: these imports of the server build resolved to nothing, and a route that reaches one fails at runtime:\n  ${unresolved.join("\n  ")}`,
    );
  }

  const files = [...traced.fileList].map((file) => join(base, file));
  const ancestor = findCommonAncestor(files);
  const links: { source: string; real: string }[] = [];
  for (const source of files) {
    const real = realpathSync(source);
    if (real !== source) {
      links.push({ source, real });
      continue;
    }
    if (statSync(source).isDirectory()) continue;
    const dest = join(functionDir, relative(ancestor, source));
    mkdirSync(dirname(dest), { recursive: true });
    copyFileSync(source, dest);
  }
  const linked: string[] = [];
  links.sort((a, b) => a.source.length - b.source.length);
  for (const { source, real } of links) {
    const rel = relative(ancestor, source);
    if (linked.some((dir) => rel.startsWith(`${dir}${sep}`))) continue;
    const directory = statSync(source).isDirectory();
    linkInsideFunction(real, join(functionDir, rel), ancestor, functionDir, directory);
    if (directory) linked.push(rel);
  }
  return { entryFile: relative(ancestor, resolve(entry)).split(sep).join("/"), ancestor };
}

async function readlinkFromRealParent(path: string): Promise<string | null> {
  let target: string;
  try {
    target = await readlink(path);
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    if (code === "EINVAL" || code === "ENOENT" || code === "UNKNOWN") return null;
    throw error;
  }
  const parent = dirname(path);
  const realParent = realpathSync(parent);
  return realParent === parent ? target : join(realParent, basename(path));
}

function linkInsideFunction(
  real: string,
  dest: string,
  ancestor: string,
  functionDir: string,
  directory: boolean,
): void {
  const placed = relative(ancestor, real);
  if (placed.startsWith("..")) {
    throw new Error(
      `ocel: the server build reaches ${real}, outside ${ancestor}, so the function cannot carry it`,
    );
  }
  mkdirSync(dirname(dest), { recursive: true });
  try {
    symlinkSync(
      relative(dirname(dest), join(functionDir, placed)),
      dest,
      directory ? "dir" : "file",
    );
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
  }
}

function findCommonAncestor(files: string[]): string {
  const [first, ...rest] = files.map((file) => dirname(file).split(sep));
  let common = first ?? [];
  for (const parts of rest) {
    let shared = 0;
    while (shared < common.length && parts[shared] === common[shared]) shared++;
    common = common.slice(0, shared);
  }
  return common.join(sep) || sep;
}
