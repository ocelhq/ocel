import { spawn } from "node:child_process";
import { cp, rm, symlink } from "node:fs/promises";
import path from "node:path";
import { redact, REDACTED } from "./contract";
import { exampleDir, ocelBin, treeDir } from "./paths";
import type { Leg } from "./spec";
import type { CellContext } from "./targets/types";
import { siblingDirs } from "./workspace";

const NEVER_COPIED = new Set([".git", ".next", ".ocel", "dist", "node_modules", "output"]);

export type Ran = { code: number | null; stdout: string; stderr: string };

export function maskArgs(args: string[]): string {
  const shown = args.map((arg, index) =>
    args[0] === "env" && args[1] === "set" && index === 3 ? REDACTED : arg,
  );
  return redact(shown.join(" "));
}

export async function copyTree(source: string, dest: string): Promise<string> {
  await rm(dest, { recursive: true, force: true });
  await cp(source, dest, {
    recursive: true,
    filter: (from) => !NEVER_COPIED.has(path.basename(from)),
  });
  await symlink(path.join(source, "node_modules"), path.join(dest, "node_modules"), "dir");
  return dest;
}

export function treeRoot(cell: CellContext, target: string): string {
  return treeDir(cell.runId, target, cell.example.name);
}

export function configTree(cell: CellContext, target: string): string {
  const root = treeRoot(cell, target);
  return siblingDirs(cell.example).length === 0 ? root : path.join(root, cell.example.dir);
}

export async function workTree(cell: CellContext, target: string): Promise<string> {
  const siblings = siblingDirs(cell.example);
  if (siblings.length === 0) {
    return copyTree(cell.dir, treeRoot(cell, target));
  }
  await rm(treeRoot(cell, target), { recursive: true, force: true });
  for (const sibling of siblings) {
    await copyTree(exampleDir(sibling), path.join(treeRoot(cell, target), sibling));
  }
  return copyTree(cell.dir, configTree(cell, target));
}

export async function spawnOcel(
  dir: string,
  args: string[],
  env: NodeJS.ProcessEnv,
): Promise<Ran> {
  return new Promise<Ran>((resolve, reject) => {
    const child = spawn(ocelBin, args, { cwd: dir, env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
}

export function exitedBadly(args: string[], result: Ran): Error {
  return new Error(
    `ocel ${maskArgs(args)} exited ${result.code}\nstdout: ${redact(result.stdout)}\nstderr: ${redact(result.stderr)}`,
  );
}

export async function ocel(dir: string, args: string[], env: NodeJS.ProcessEnv): Promise<Ran> {
  const result = await spawnOcel(dir, args, env);
  if (result.code !== 0) {
    throw exitedBadly(args, result);
  }
  return result;
}

export async function runOcel(
  cell: CellContext,
  dir: string,
  leg: Leg,
  name: string,
  args: string[],
  env: NodeJS.ProcessEnv,
): Promise<Ran> {
  const result = await spawnOcel(dir, args, env);
  await cell.evidence.write(leg, `${name}.stdout`, result.stdout);
  await cell.evidence.write(leg, `${name}.stderr`, result.stderr);
  if (result.code !== 0) {
    throw exitedBadly(args, result);
  }
  return result;
}
