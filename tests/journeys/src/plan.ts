import {
  type Cell,
  type FixtureSpec,
  fixtureNameOf,
  ladderTitle,
  type Leg,
  legsOf,
  variantNameOf,
} from "./spec";

export const UP_TITLE = "up";
export const DESTROY_TITLE = "destroy";
export const REDEPLOY_TITLE = "redeploy";
export const ROLLBACK_TITLE = "rollback";
export const REFUSE_TITLE = "refuse";

export type PlannedTest = {
  cell: string;
  fixture: string;
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
  fixture: FixtureSpec,
  leg: "contract" | "redeploy" | "rollback",
): Array<{ title: string; leg: Leg }> {
  return (fixture.hooks?.rows ?? [])
    .filter((row) => row.phase === "consume")
    .map((row) => ({ title: ladderConsumeTitle(leg, row.title), leg }));
}

function titlesForLeg(cell: Cell, leg: Leg): Array<{ title: string; leg: Leg }> {
  const { fixture } = cell;
  const rows = fixture.rows.map((row) => ({
    title: contractTitle(leg, row.title),
    leg,
  }));
  switch (leg) {
    case "up":
      return [{ title: UP_TITLE, leg }];
    case "destroy":
      return [{ title: DESTROY_TITLE, leg }];
    case "contract":
      return [...rows, ...ladderConsumeRows(fixture, "contract")];
    case "redeploy":
      return [{ title: REDEPLOY_TITLE, leg }, ...rows, ...ladderConsumeRows(fixture, "redeploy")];
    case "rollback":
      return [{ title: ROLLBACK_TITLE, leg }, ...rows, ...ladderConsumeRows(fixture, "rollback")];
  }
}

function ladderPhaseTitles(fixture: FixtureSpec): Array<{ title: string }> {
  const hooks = fixture.hooks;
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

export function planTests(cells: Cell[], able: Leg[]): PlannedTest[] {
  const planned: PlannedTest[] = [];
  for (const cell of cells) {
    const { fixture } = cell;
    const shared = { fixture: fixtureNameOf(fixture), variant: variantNameOf(cell) };
    for (const app of fixture.apps) {
      for (const leg of legsOf(fixture, able)) {
        for (const entry of titlesForLeg(cell, leg)) {
          planned.push({ cell: cellKey(cell.name, app), app, ...shared, ...entry });
        }
      }
    }
    const [firstApp] = fixture.apps;
    if (firstApp) {
      for (const entry of ladderPhaseTitles(fixture)) {
        planned.push({ cell: cellKey(cell.name, firstApp), app: firstApp, ...shared, ...entry });
      }
    }
  }
  return planned;
}
