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

export function laneDir(runId: string, target: string): string {
  return path.join(outputRoot, runId, target);
}

export function evidenceDir(runId: string, target: string, example: string): string {
  return path.join(laneDir(runId, target), example);
}

export function treeDir(runId: string, target: string, example: string): string {
  return path.join(laneDir(runId, target), "trees", example);
}

export function cellsDir(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "cells");
}

export function resultsFile(runId: string, target: string, cell: string): string {
  return path.join(cellsDir(runId, target), `${cell.replace(/\//g, "__")}.jsonl`);
}

export function prepareFile(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "prepare.json");
}
