import { contractRows } from "./contract";
import {
  cellNameOf,
  type ExampleSpec,
  ladderTitle,
  type Leg,
  type Offered,
  suitesOf,
  type Variant,
  variantsOf,
} from "./spec";

export const UP_TITLE = "up";
export const DESTROY_TITLE = "destroy";
export const REDEPLOY_TITLE = "redeploy";
export const ROLLBACK_TITLE = "rollback";
export const REFUSE_TITLE = "refuse";

export type PlannedTest = { cell: string; title: string; leg?: Leg; variant: Variant };

export function cellKey(example: string, app: string): string {
  return `${example}/${app}`;
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

function titlesForLeg(
  example: ExampleSpec,
  variant: Variant,
  leg: Leg,
): Array<{ title: string; leg: Leg }> {
  const rows = contractRows(suitesOf(example, variant.mode)).map((row) => ({
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

export function planTests(examples: ExampleSpec[], legs: Leg[], offered: Offered): PlannedTest[] {
  const planned: PlannedTest[] = [];
  for (const example of examples) {
    for (const variant of variantsOf(example, offered)) {
      const name = cellNameOf(example, variant, offered);
      for (const app of example.apps) {
        for (const leg of legs) {
          for (const entry of titlesForLeg(example, variant, leg)) {
            planned.push({ cell: cellKey(name, app), variant, ...entry });
          }
        }
      }
      const [firstApp] = example.apps;
      if (firstApp) {
        for (const entry of ladderPhaseTitles(example)) {
          planned.push({ cell: cellKey(name, firstApp), variant, ...entry });
        }
      }
    }
  }
  return planned;
}
