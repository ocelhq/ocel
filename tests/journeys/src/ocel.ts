import { spawn } from "node:child_process";
import path from "node:path";
import { redact, REDACTED } from "./contract";
import { ocelBin, treeDir } from "./paths";
import type { Leg } from "./spec";
import type { CellContext } from "./targets/types";
import { plantWorkspace } from "./tree";
import { siblingDirs } from "./workspace";

export type Ran = { code: number | null; stdout: string; stderr: string };

export function maskArgs(args: string[]): string {
  const shown = args.map((arg, index) =>
    args[0] === "env" && args[1] === "set" && index === 3 ? REDACTED : arg,
  );
  return redact(shown.join(" "));
}

export function treeRoot(cell: CellContext, target: string): string {
  return treeDir(cell.runId, target, cell.example.name);
}

export function configTree(cell: CellContext, target: string): string {
  return path.join(treeRoot(cell, target), cell.example.dir);
}

export function appDirs(cell: CellContext): string[] {
  const siblings = siblingDirs(cell.example);
  return siblings.length === 0 ? [cell.example.dir] : [cell.example.dir, ...siblings];
}

export async function workTree(cell: CellContext, target: string): Promise<string> {
  await plantWorkspace(treeRoot(cell, target), `journey-${cell.example.name}`, appDirs(cell));
  return configTree(cell, target);
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
