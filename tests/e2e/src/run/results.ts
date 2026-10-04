import { appendFileSync, mkdirSync } from "node:fs";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { cellsDir, resultsFile } from "../paths";

export type StepResult = {
  cell: string;
  title: string;
  outcome: "passed" | "failed";
  error?: string;
  startTime: number;
  duration: number;
};

export function resultWriter(
  runId: string,
  target: string,
  cell: string,
): (result: StepResult) => void {
  const file = resultsFile(runId, target, cell);
  mkdirSync(path.dirname(file), { recursive: true });
  return (result) => appendFileSync(file, `${JSON.stringify(result)}\n`, "utf8");
}

export async function readResults(runId: string, target: string): Promise<StepResult[]> {
  const dir = cellsDir(runId, target);
  let names: string[];
  try {
    names = await readdir(dir);
  } catch {
    return [];
  }
  const results: StepResult[] = [];
  for (const name of names.filter((entry) => entry.endsWith(".jsonl")).sort()) {
    const read = await readFile(path.join(dir, name), "utf8");
    for (const line of read.split("\n")) {
      if (line.trim() !== "") {
        results.push(JSON.parse(line) as StepResult);
      }
    }
  }
  return results;
}
