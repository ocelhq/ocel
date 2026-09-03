import { contractRows } from "../contract";
import { contractTitle } from "../plan";
import type { Leg, Suite } from "../spec";

export const CONTRACT_LEGS: Leg[] = ["contract", "redeploy", "rollback"];

export function legTitles(title: string, legs: Leg[], issue: string): Record<string, string> {
  return Object.fromEntries(legs.map((leg) => [contractTitle(leg, title), issue]));
}

export function suiteTitles(suite: Suite, legs: Leg[], issue: string): Record<string, string> {
  const listed: Record<string, string> = {};
  for (const row of contractRows([suite])) {
    Object.assign(listed, legTitles(row.title, legs, issue));
  }
  return listed;
}
