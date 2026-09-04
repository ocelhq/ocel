import type { ExampleSpec, Kind } from "./spec";

const RANK: Record<Kind, number> = {
  ladder: 0,
  composite: 1,
  workspace: 2,
};

export function longestFirst(rows: ExampleSpec[]): ExampleSpec[] {
  return [...rows].sort((a, b) => RANK[a.kind] - RANK[b.kind]);
}
