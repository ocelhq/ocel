import { appendFileSync, mkdirSync } from "node:fs";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { cellsDir, resultsFile } from "./paths";

export type RecordedRow = {
  cell: string;
  title: string;
  outcome: "passed" | "failed";
  error?: string;
  startTime: number;
  duration: number;
};

export function ledgerFor(runId: string, target: string, cell: string): (row: RecordedRow) => void {
  const file = resultsFile(runId, target, cell);
  mkdirSync(path.dirname(file), { recursive: true });
  return (row) => appendFileSync(file, `${JSON.stringify(row)}\n`, "utf8");
}

export async function readRows(runId: string, target: string): Promise<RecordedRow[]> {
  const dir = cellsDir(runId, target);
  let names: string[];
  try {
    names = await readdir(dir);
  } catch {
    return [];
  }
  const rows: RecordedRow[] = [];
  for (const name of names.filter((entry) => entry.endsWith(".jsonl")).sort()) {
    const read = await readFile(path.join(dir, name), "utf8");
    for (const line of read.split("\n")) {
      if (line.trim() !== "") {
        rows.push(JSON.parse(line) as RecordedRow);
      }
    }
  }
  return rows;
}
