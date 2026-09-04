import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export const packageRoot = path.resolve(here, "..");
export const repoRoot = path.resolve(packageRoot, "..", "..");
export const examplesDir = path.join(repoRoot, "examples");
export const outputRoot = path.join(packageRoot, "output");

export const ocelBin = process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "bin", "ocel");

export function exampleMember(dir: string): string {
  return path.posix.join("examples", dir);
}

export function exampleDir(dir: string): string {
  return path.join(examplesDir, dir);
}

export function laneEdge(target: string, env: NodeJS.ProcessEnv = process.env): string | undefined {
  const edge = env.OCEL_AWS_EDGE?.trim();
  return target === "aws" && edge ? edge : undefined;
}

export function laneName(target: string, env: NodeJS.ProcessEnv = process.env): string {
  const edge = laneEdge(target, env);
  return edge ? `${target}-${edge}` : target;
}

export function laneDir(runId: string, target: string): string {
  return path.join(outputRoot, runId, laneName(target));
}

export function evidenceDir(runId: string, target: string, example: string): string {
  return path.join(laneDir(runId, target), example);
}

export function treeDir(runId: string, target: string, example: string): string {
  return path.join(laneDir(runId, target), "trees", example);
}

export function verdictFile(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "verdict.json");
}

export function prepareFile(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "prepare.json");
}
