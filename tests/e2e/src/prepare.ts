import { readFileSync } from "node:fs";
import { prepareFile } from "./paths";

export type PrepareFailures = { lane?: string };

export type PrepareRecord = { ms: number; failures: PrepareFailures };

export function readPrepared(runId: string, target: string): PrepareRecord | undefined {
  try {
    const read = JSON.parse(readFileSync(prepareFile(runId, target), "utf8")) as PrepareRecord;
    return { ms: typeof read.ms === "number" ? read.ms : 0, failures: read.failures ?? {} };
  } catch {
    return undefined;
  }
}

export function readPrepareFailure(runId: string, target: string): string | undefined {
  return readPrepared(runId, target)?.failures.lane;
}
