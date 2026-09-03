import { contractRows } from "./contract";
import type { ExampleSpec, Leg } from "./spec";

export const UP_TITLE = "up";
export const DESTROY_TITLE = "destroy";
export const REDEPLOY_TITLE = "redeploy";
export const ROLLBACK_TITLE = "rollback";

export type PlannedTest = { cell: string; title: string; leg: Leg };

export function cellKey(example: string, app: string): string {
  return `${example}/${app}`;
}

export function contractTitle(leg: Leg, title: string): string {
  return leg === "contract" ? title : `${leg} · ${title}`;
}

function titlesForLeg(example: ExampleSpec, leg: Leg): Array<{ title: string; leg: Leg }> {
  const rows = contractRows(example.suites).map((row) => ({
    title: contractTitle(leg, row.title),
    leg,
  }));
  switch (leg) {
    case "up":
      return [{ title: UP_TITLE, leg }];
    case "destroy":
      return [{ title: DESTROY_TITLE, leg }];
    case "contract":
      return rows;
    case "redeploy":
      return [{ title: REDEPLOY_TITLE, leg }, ...rows];
    case "rollback":
      return [{ title: ROLLBACK_TITLE, leg }, ...rows];
  }
}

export function planTests(examples: ExampleSpec[], legs: Leg[]): PlannedTest[] {
  const planned: PlannedTest[] = [];
  for (const example of examples) {
    for (const app of example.apps) {
      for (const leg of legs) {
        for (const entry of titlesForLeg(example, leg)) {
          planned.push({ cell: cellKey(example.name, app), ...entry });
        }
      }
    }
  }
  return planned;
}
