import { contractRows } from "./contract";
import { type Cell, type ExampleSpec, ladderTitle, type Leg, suitesOf, variantNameOf } from "./spec";

export const UP_TITLE = "up";
export const DESTROY_TITLE = "destroy";
export const REDEPLOY_TITLE = "redeploy";
export const ROLLBACK_TITLE = "rollback";
export const REFUSE_TITLE = "refuse";

export type PlannedTest = {
  cell: string;
  example: string;
  app: string;
  variant: string;
  title: string;
  leg?: Leg;
};

export function cellKey(cell: string, app: string): string {
  return `${cell}/${app}`;
}

export function contractTitle(leg: Leg, title: string): string {
  return leg === "contract" ? title : `${leg} · ${title}`;
}

export function ladderConsumeTitle(leg: "contract" | "redeploy" | "rollback", title: string): string {
  const base = ladderTitle("consume", title);
  return leg === "contract" ? base : `${leg} · ${base}`;
}

function ladderConsumeRows(
  example: ExampleSpec,
  leg: "contract" | "redeploy" | "rollback",
): Array<{ title: string; leg: Leg }> {
  return (example.hooks?.rows ?? [])
    .filter((row) => row.phase === "consume")
    .map((row) => ({ title: ladderConsumeTitle(leg, row.title), leg }));
}

function titlesForLeg(cell: Cell, leg: Leg): Array<{ title: string; leg: Leg }> {
  const { example } = cell;
  const rows = contractRows(suitesOf(example, cell.variant)).map((row) => ({
    title: contractTitle(leg, row.title),
    leg,
  }));
  switch (leg) {
    case "up":
      return [{ title: UP_TITLE, leg }];
    case "destroy":
      return [{ title: DESTROY_TITLE, leg }];
    case "contract":
      return [...rows, ...ladderConsumeRows(example, "contract")];
    case "redeploy":
      return [{ title: REDEPLOY_TITLE, leg }, ...rows, ...ladderConsumeRows(example, "redeploy")];
    case "rollback":
      return [{ title: ROLLBACK_TITLE, leg }, ...rows, ...ladderConsumeRows(example, "rollback")];
  }
}

function ladderPhaseTitles(example: ExampleSpec): Array<{ title: string }> {
  const hooks = example.hooks;
  if (!hooks) {
    return [];
  }
  const out: Array<{ title: string }> = [];
  if (hooks.refuse) {
    out.push({ title: REFUSE_TITLE });
  }
  for (const row of hooks.rows ?? []) {
    if (row.phase === "publish" || row.phase === "outlive" || row.phase === "prune") {
      out.push({ title: ladderTitle(row.phase, row.title) });
    }
  }
  return out;
}

export function planTests(cells: Cell[], legs: Leg[]): PlannedTest[] {
  const planned: PlannedTest[] = [];
  for (const cell of cells) {
    const { example } = cell;
    const shared = { example: example.name, variant: variantNameOf(cell) };
    for (const app of example.apps) {
      for (const leg of legs) {
        for (const entry of titlesForLeg(cell, leg)) {
          planned.push({ cell: cellKey(cell.name, app), app, ...shared, ...entry });
        }
      }
    }
    const [firstApp] = example.apps;
    if (firstApp) {
      for (const entry of ladderPhaseTitles(example)) {
        planned.push({ cell: cellKey(cell.name, firstApp), app: firstApp, ...shared, ...entry });
      }
    }
  }
  return planned;
}
