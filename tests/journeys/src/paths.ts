import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export const packageRoot = path.resolve(here, "..");
export const repoRoot = path.resolve(packageRoot, "..", "..");
export const examplesDir = path.join(repoRoot, "examples");
export const outputRoot = path.join(packageRoot, "output");

export const ocelBin = process.env.OCEL_BIN ?? path.join(repoRoot, "cli", "bin", "ocel");

export function exampleDir(dir: string): string {
  return path.join(examplesDir, dir);
}

export function evidenceDir(runId: string, target: string, example: string): string {
  return path.join(outputRoot, runId, target, example);
}
