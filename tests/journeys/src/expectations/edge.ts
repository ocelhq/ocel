import { type PlannedTest, planTests, UP_TITLE } from "../plan";
import { specForTarget } from "../spec";
import type { Expectations } from "./types";

export const EDGE_ENV = "OCEL_AWS_EDGE";

export const DEFAULT_EDGE = "cloudfront";

const SST_UTIL = "https://github.com/ocelhq/ocel/issues/857";
const PULUMI_SERIALIZATION = "https://github.com/ocelhq/ocel/issues/856";

const LADDER_ISSUES: Record<string, string> = {
  "with-sst": SST_UTIL,
  "with-pulumi": PULUMI_SERIALIZATION,
};

export function exampleOf(cell: string): string {
  const [example] = cell.split("/");
  return example ?? "";
}

export function ladderIssueFor(cell: string): string | undefined {
  return LADDER_ISSUES[exampleOf(cell)];
}

export function upTests(): PlannedTest[] {
  return planTests(specForTarget("aws"), ["up"]).filter((test) => test.title === UP_TITLE);
}

export function forEdge(
  byEdge: Record<string, () => Expectations>,
  edge: string | undefined,
): Expectations {
  const named = edge || DEFAULT_EDGE;
  const listed = byEdge[named];
  if (!listed) {
    throw new Error(
      `${EDGE_ENV} is ${named}, and this file lists no cells for it (${Object.keys(byEdge).join(", ")})`,
    );
  }
  return listed();
}
