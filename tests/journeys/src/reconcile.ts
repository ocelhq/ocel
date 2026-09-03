import type { Expectations } from "./expectations/types";
import { type PlannedTest, UP_TITLE } from "./plan";
import type { Leg } from "./spec";

export type TestOutcome = "passed" | "failed" | "skipped" | "todo" | "only";

export type TestResult = {
  cell: string;
  title: string;
  outcome: TestOutcome;
  error?: string;
};

export type Verdict =
  | "ok"
  | "expected-failure"
  | "blocked"
  | "unexpected-failure"
  | "listed-and-passed"
  | "never-ran"
  | "disabled"
  | "unplanned";

export type ReportRow = {
  cell: string;
  title: string;
  leg?: Leg;
  verdict: Verdict;
  issue?: string;
  error?: string;
};

export type Report = {
  rows: ReportRow[];
  failures: ReportRow[];
  failed: boolean;
};

const FAILING_VERDICTS: ReadonlySet<Verdict> = new Set<Verdict>([
  "unexpected-failure",
  "listed-and-passed",
  "never-ran",
  "disabled",
  "unplanned",
]);

const DISABLED: ReadonlySet<TestOutcome> = new Set<TestOutcome>([
  "skipped",
  "todo",
  "only",
]);

export function exitCodeFor(verdicts: Iterable<Verdict>): number {
  for (const verdict of verdicts) {
    if (FAILING_VERDICTS.has(verdict)) {
      return 1;
    }
  }
  return 0;
}

function key(cell: string, title: string): string {
  return JSON.stringify([cell, title]);
}

type Blocking = "none" | "up-failed" | "up-listed";

function verdictFor(
  result: TestResult | undefined,
  issue: string | undefined,
  blocking: Blocking,
): Verdict {
  const listed = blocking === "up-listed";
  if (!result) {
    return listed ? "blocked" : "never-ran";
  }
  if (DISABLED.has(result.outcome)) {
    return "disabled";
  }
  if (result.outcome === "failed") {
    if (issue) {
      return "expected-failure";
    }
    return blocking === "none" ? "unexpected-failure" : "blocked";
  }
  if (issue) {
    return listed ? "ok" : "listed-and-passed";
  }
  return "ok";
}

function blockingByCell(
  planned: PlannedTest[],
  byKey: Map<string, TestResult>,
  expectations: Expectations,
): Map<string, Blocking> {
  const blocking = new Map<string, Blocking>();
  for (const cell of new Set(planned.map((entry) => entry.cell))) {
    const up = byKey.get(key(cell, UP_TITLE));
    if (up?.outcome !== "failed") {
      continue;
    }
    blocking.set(cell, expectations[cell]?.[UP_TITLE] ? "up-listed" : "up-failed");
  }
  return blocking;
}

export function reconcile(input: {
  planned: PlannedTest[];
  results: TestResult[];
  expectations: Expectations;
}): Report {
  const byKey = new Map(input.results.map((result) => [key(result.cell, result.title), result]));
  const blocking = blockingByCell(input.planned, byKey, input.expectations);
  const seen = new Set<string>();

  const rows: ReportRow[] = input.planned.map((entry) => {
    const id = key(entry.cell, entry.title);
    seen.add(id);
    const result = byKey.get(id);
    const issue = input.expectations[entry.cell]?.[entry.title];
    const downstream: Blocking =
      entry.title === UP_TITLE ? "none" : (blocking.get(entry.cell) ?? "none");
    return {
      cell: entry.cell,
      title: entry.title,
      leg: entry.leg,
      verdict: verdictFor(result, issue, downstream),
      issue,
      error: result?.error,
    };
  });

  for (const result of input.results) {
    if (!seen.has(key(result.cell, result.title))) {
      rows.push({
        cell: result.cell,
        title: result.title,
        verdict: "unplanned",
        error: result.error,
      });
    }
  }

  const failures = rows.filter((row) => FAILING_VERDICTS.has(row.verdict));
  return { rows, failures, failed: failures.length > 0 };
}
