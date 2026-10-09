import { realpathSync } from "node:fs";
import { lstat, readlink, rm } from "node:fs/promises";
import path from "node:path";
import { nodeFileTrace } from "@vercel/nft";
import { type BuildOutput, toSlash } from "./output.mjs";

export interface TraceLog {
  warn(message: string): void;
}

export async function traceIntoFunction(
  output: BuildOutput,
  id: string,
  entry: string,
  log: TraceLog,
): Promise<string> {
  await rm(output.functionDir(id), { force: true, recursive: true });
  const base = path.parse(path.resolve(entry)).root;
  const traced = await nodeFileTrace([entry], {
    base,
    processCwd: process.cwd(),
    ignore: (file) => file.startsWith("**"),
    readlink: readlinkFromRealParent,
  });
  warnUnresolved(traced.warnings, log);

  const files: string[] = [];
  for (const file of [...traced.fileList].sort()) {
    const source = path.join(base, file);
    if ((await lstat(source)).isDirectory()) continue;
    files.push(source);
  }
  const ancestor = findCommonAncestor(files);
  await output.copyIntoFunction(
    id,
    Object.fromEntries(files.map((source) => [toSlash(path.relative(ancestor, source)), source])),
    ancestor,
  );
  return toSlash(path.relative(ancestor, path.resolve(entry)));
}

function warnUnresolved(warnings: Iterable<Error>, log: TraceLog): void {
  const unresolved: string[] = [];
  for (const warning of warnings) {
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
}

async function readlinkFromRealParent(file: string): Promise<string | null> {
  let target: string;
  try {
    target = await readlink(file);
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    if (code === "EINVAL" || code === "ENOENT" || code === "UNKNOWN") return null;
    throw error;
  }
  const parent = path.dirname(file);
  const realParent = realpathSync(parent);
  return realParent === parent ? target : path.join(realParent, path.basename(file));
}

function findCommonAncestor(files: string[]): string {
  const [first, ...rest] = files.map((file) => path.dirname(file).split(path.sep));
  let common = first ?? [];
  for (const parts of rest) {
    let shared = 0;
    while (shared < common.length && parts[shared] === common[shared]) shared++;
    common = common.slice(0, shared);
  }
  return common.join(path.sep) || path.sep;
}
