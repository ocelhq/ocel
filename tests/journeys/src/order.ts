import type { Cell, Concern, Kind } from "./spec";

const CONCERN_RANK: Record<Concern, number> = {
  deploy: 0,
  sdk: 1,
};

const KIND_RANK: Record<Kind, number> = {
  ladder: 0,
  composite: 1,
  workspace: 2,
};

function rank(cell: Cell): number {
  return CONCERN_RANK[cell.fixture.concern] * 10 + KIND_RANK[cell.fixture.kind];
}

export function longestFirst(cells: Cell[]): Cell[] {
  return [...cells].sort((a, b) => rank(a) - rank(b));
}
