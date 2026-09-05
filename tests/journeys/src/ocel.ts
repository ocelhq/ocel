import { spawn } from "node:child_process";
import path from "node:path";
import { shapeFor, writeJourneyConfig } from "./config";
import { redact, REDACTED } from "./contract";
import { live, relay, type Say } from "./live";
import { fixtureMember, ocelBin, treeDir } from "./paths";
import type { Leg, TargetName } from "./spec";
import type { CellContext } from "./targets/types";
import { plantWorkspace } from "./tree";

export type Ran = { code: number | null; stdout: string; stderr: string };

export const COMMAND_LOG = "commands.jsonl";

export function maskArgs(args: string[]): string {
  const shown = args.map((arg, index) =>
    args[0] === "env" && args[1] === "set" && index === 3 ? REDACTED : arg,
  );
  return redact(shown.join(" "));
}

export function treeRoot(cell: CellContext, target: string): string {
  return treeDir(cell.runId, target, cell.name);
}

export function configTree(cell: CellContext, target: string): string {
  return path.join(treeRoot(cell, target), fixtureMember(cell.fixture.dir));
}

export function appDirs(cell: CellContext): string[] {
  return [fixtureMember(cell.fixture.dir)];
}

export async function workTree(cell: CellContext, target: TargetName): Promise<string> {
  await plantWorkspace(treeRoot(cell, target), `journey-${cell.name}`, appDirs(cell));
  const dir = configTree(cell, target);
  await writeJourneyConfig(dir, shapeFor(cell, target, process.env));
  return dir;
}

export async function spawnOcel(
  dir: string,
  args: string[],
  env: NodeJS.ProcessEnv,
  say: Say = live("ocel |"),
): Promise<Ran> {
  return new Promise<Ran>((resolve, reject) => {
    const child = spawn(ocelBin, args, { cwd: dir, env });
    let stdout = "";
    let stderr = "";
    say(`$ ocel ${maskArgs(args)}`);
    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    relay(child.stdout, say);
    relay(child.stderr, say);
    child.on("error", reject);
    child.on("close", (code) => {
      say(`exited ${code}`);
      resolve({ code, stdout, stderr });
    });
  });
}

export function exitedBadly(args: string[], result: Ran): Error {
  return new Error(
    `ocel ${maskArgs(args)} exited ${result.code}\nstdout: ${redact(result.stdout)}\nstderr: ${redact(result.stderr)}`,
  );
}

export async function ocel(
  dir: string,
  args: string[],
  env: NodeJS.ProcessEnv,
  say?: Say,
): Promise<Ran> {
  const result = await spawnOcel(dir, args, env, say);
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
  const began = Date.now();
  const result = await spawnOcel(dir, args, env, live(`${cell.name} ${leg}/${name} |`));
  await cell.evidence.append(
    COMMAND_LOG,
    JSON.stringify({ leg, name, ms: Date.now() - began, code: result.code }),
  );
  await cell.evidence.write(leg, `${name}.stdout`, result.stdout);
  await cell.evidence.write(leg, `${name}.stderr`, result.stderr);
  if (result.code !== 0) {
    throw exitedBadly(args, result);
  }
  return result;
}
