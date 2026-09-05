import type { Cell, Kind } from "./spec";

const RANK: Record<Kind, number> = {
  ladder: 0,
  composite: 1,
  workspace: 2,
};

export function longestFirst(cells: Cell[]): Cell[] {
  return [...cells].sort((a, b) => RANK[a.example.kind] - RANK[b.example.kind]);
}
