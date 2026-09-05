import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export const packageRoot = path.resolve(here, "..");
export const repoRoot = path.resolve(packageRoot, "..", "..");
export const fixturesDir = path.join(repoRoot, "tests", "fixtures");
export const outputRoot = path.join(packageRoot, "output");

export const ocelBin = process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "bin", "ocel");

export function fixtureMember(dir: string): string {
  return path.posix.join("tests", "fixtures", dir);
}

export function fixtureDir(dir: string): string {
  return path.join(fixturesDir, dir);
}

export function laneDir(runId: string, target: string): string {
  return path.join(outputRoot, runId, target);
}

export function fileNameOf(cell: string): string {
  return cell.replace(/\//g, "__");
}

export function evidenceDir(runId: string, target: string, cell: string): string {
  return path.join(laneDir(runId, target), fileNameOf(cell));
}

export function treeDir(runId: string, target: string, cell: string): string {
  return path.join(laneDir(runId, target), "trees", fileNameOf(cell));
}

export function cellsDir(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "cells");
}

export function resultsFile(runId: string, target: string, cell: string): string {
  return path.join(cellsDir(runId, target), `${fileNameOf(cell)}.jsonl`);
}

export function prepareFile(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "prepare.json");
}

export function cellFilesDir(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "files");
}

export function cellFile(runId: string, target: string, cell: string): string {
  return path.join(cellFilesDir(runId, target), `${fileNameOf(cell)}.journey.test.ts`);
}
