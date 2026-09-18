import { spawn } from "node:child_process";
import path from "node:path";
import { REDACTED, redact } from "./checks/context";
import { overlayFor, writeJourneyConfig } from "./config";
import type { Phase, TargetName } from "./matrix/types";
import { fixtureMember, ocelBin, providersDir, treeDir } from "./paths";
import { type Log, progress, relay } from "./progress";
import type { CellUnderTest } from "./run/cellRun";
import { plantWorkspace } from "./tree";

export type Ran = { code: number | null; stdout: string; stderr: string };

export const COMMAND_LOG = "commands.jsonl";

export function maskArgs(args: string[]): string {
  const shown = args.map((arg, index) =>
    args[0] === "env" && args[1] === "set" && index === 3 ? REDACTED : arg,
  );
  return redact(shown.join(" "));
}

export function treeRoot(cell: CellUnderTest, target: string): string {
  return treeDir(cell.runId, target, cell.name);
}

export function configTree(cell: CellUnderTest, target: string): string {
  return path.join(treeRoot(cell, target), fixtureMember(cell.fixture.name));
}

export function appDirs(cell: CellUnderTest): string[] {
  return [fixtureMember(cell.fixture.name)];
}

export async function workTree(cell: CellUnderTest, target: TargetName): Promise<string> {
  await plantWorkspace(treeRoot(cell, target), `journey-${cell.name}`, appDirs(cell));
  const dir = configTree(cell, target);
  await writeJourneyConfig(dir, overlayFor(cell, target, process.env));
  return dir;
}

export async function spawnOcel(
  dir: string,
  args: string[],
  env: NodeJS.ProcessEnv,
  log: Log = progress("ocel |"),
): Promise<Ran> {
  return new Promise<Ran>((resolve, reject) => {
    const child = spawn(ocelBin, args, {
      cwd: dir,
      env: { OCEL_PROVIDERS_DIR: providersDir, ...env },
    });
    let stdout = "";
    let stderr = "";
    log(`$ ocel ${maskArgs(args)}`);
    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    relay(child.stdout, log);
    relay(child.stderr, log);
    child.on("error", reject);
    child.on("close", (code) => {
      log(`exited ${code}`);
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
  log?: Log,
): Promise<Ran> {
  const result = await spawnOcel(dir, args, env, log);
  if (result.code !== 0) {
    throw exitedBadly(args, result);
  }
  return result;
}

export async function runOcel(
  cell: CellUnderTest,
  dir: string,
  phase: Phase,
  name: string,
  args: string[],
  env: NodeJS.ProcessEnv,
): Promise<Ran> {
  const began = Date.now();
  const result = await spawnOcel(dir, args, env, progress(`${cell.name} ${phase}/${name} |`));
  await cell.evidence.append(
    COMMAND_LOG,
    JSON.stringify({ phase, name, ms: Date.now() - began, code: result.code }),
  );
  await cell.evidence.write(phase, `${name}.stdout`, result.stdout);
  await cell.evidence.write(phase, `${name}.stderr`, result.stderr);
  if (result.code !== 0) {
    throw exitedBadly(args, result);
  }
  return result;
}
