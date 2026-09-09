import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export const packageRoot = path.resolve(here, "..");
export const repoRoot = path.resolve(packageRoot, "..", "..");
export const fixturesDir = path.join(repoRoot, "tests", "fixtures");
export const outputRoot = path.join(packageRoot, "output");

const snapshotDir = path.join(repoRoot, "dist");

function snapshotCli(): string {
  try {
    const artifacts = JSON.parse(
      readFileSync(path.join(snapshotDir, "artifacts.json"), "utf8"),
    ) as { type: string; path: string; extra: { ID: string } }[];
    const built = artifacts.find((a) => a.type === "Binary" && a.extra.ID === "ocel");
    if (built) return path.join(repoRoot, built.path);
  } catch {}
  return path.join(snapshotDir, "ocel");
}

export const ocelBin = process.env.OCEL_BIN ?? snapshotCli();

export const providersDir = process.env.OCEL_PROVIDERS_DIR ?? path.join(snapshotDir, "providers");

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

export function liveFile(runId: string, target: string): string {
  return path.join(laneDir(runId, target), "live.log");
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
