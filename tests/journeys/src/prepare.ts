import { readFileSync } from "node:fs";
import { prepareFile } from "./paths";
import type { Edge } from "./spec";

export type PrepareFailures = {
  lane?: string;
  edges?: { [K in Edge]?: string };
};

export type PrepareRecord = { ms: number; failures: PrepareFailures };

export function failureFor(
  failures: PrepareFailures,
  edge: Edge | undefined,
): string | undefined {
  if (failures.lane) {
    return failures.lane;
  }
  return edge === undefined ? undefined : failures.edges?.[edge];
}

export function readPrepared(runId: string, target: string): PrepareRecord | undefined {
  try {
    const read = JSON.parse(readFileSync(prepareFile(runId, target), "utf8")) as PrepareRecord;
    return { ms: typeof read.ms === "number" ? read.ms : 0, failures: read.failures ?? {} };
  } catch {
    return undefined;
  }
}

export function readPrepareFailures(runId: string, target: string): PrepareFailures {
  return readPrepared(runId, target)?.failures ?? {};
}
