import { createHash } from "node:crypto";
import { type ExampleSpec, preferredOf } from "./spec";

export type Pick = { seed: string; touched: string[] };

function memberFor(seed: string, group: string, members: ExampleSpec[]): ExampleSpec {
  const digest = createHash("sha256").update(`${seed}:${group}`).digest();
  return members[digest.readUInt32BE(0) % members.length];
}

function untouched(seed: string, group: string, members: ExampleSpec[]): ExampleSpec {
  const preferred = members.find((row) => row.name === preferredOf(group));
  return preferred ?? memberFor(seed, group, members);
}

export function pickExamples(
  rows: ExampleSpec[],
  pick: Pick | undefined,
): { chosen: ExampleSpec[]; leftOut: ExampleSpec[] } {
  if (!pick) {
    return { chosen: rows, leftOut: [] };
  }

  const groups = new Map<string, ExampleSpec[]>();
  for (const row of rows) {
    if (row.group === undefined) {
      continue;
    }
    const members = groups.get(row.group) ?? [];
    members.push(row);
    groups.set(row.group, members);
  }

  const running = new Set<ExampleSpec>();
  for (const [group, members] of groups) {
    const touched = members.filter((row) => pick.touched.includes(row.dir));
    for (const row of touched.length > 0 ? touched : [untouched(pick.seed, group, members)]) {
      running.add(row);
    }
  }

  const chosen = rows.filter((row) => row.group === undefined || running.has(row));
  const leftOut = rows.filter((row) => row.group !== undefined && !running.has(row));
  return { chosen, leftOut };
}
