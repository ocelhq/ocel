import type { ExampleSpec, Kind } from "./spec";

const RANK: Record<Kind, number> = {
  ladder: 0,
  hello: 1,
  composite: 2,
  workspace: 3,
};

export const MODULE_SUFFIX = ".journey.test.ts";

export function longestFirst(rows: ExampleSpec[]): ExampleSpec[] {
  return [...rows].sort((a, b) => RANK[a.kind] - RANK[b.kind]);
}

export function exampleOf(basename: string): string {
  return basename.endsWith(MODULE_SUFFIX)
    ? basename.slice(0, -MODULE_SUFFIX.length)
    : basename;
}
