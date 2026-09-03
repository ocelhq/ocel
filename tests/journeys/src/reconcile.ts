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

function verdictFor(
  result: TestResult | undefined,
  issue: string | undefined,
  blocked: boolean,
): Verdict {
  if (!result) {
    return blocked ? "blocked" : "never-ran";
  }
  if (DISABLED.has(result.outcome)) {
    return "disabled";
  }
  if (result.outcome === "failed") {
    if (issue) {
      return "expected-failure";
    }
    return blocked ? "blocked" : "unexpected-failure";
  }
  if (issue) {
    return blocked ? "ok" : "listed-and-passed";
  }
  return "ok";
}

function blockedCells(
  planned: PlannedTest[],
  byKey: Map<string, TestResult>,
  expectations: Expectations,
): Set<string> {
  const blocked = new Set<string>();
  for (const cell of new Set(planned.map((entry) => entry.cell))) {
    const up = byKey.get(key(cell, UP_TITLE));
    if (up?.outcome === "failed" && expectations[cell]?.[UP_TITLE]) {
      blocked.add(cell);
    }
  }
  return blocked;
}

export function reconcile(input: {
  planned: PlannedTest[];
  results: TestResult[];
  expectations: Expectations;
}): Report {
  const byKey = new Map(input.results.map((result) => [key(result.cell, result.title), result]));
  const blocked = blockedCells(input.planned, byKey, input.expectations);
  const seen = new Set<string>();

  const rows: ReportRow[] = input.planned.map((entry) => {
    const id = key(entry.cell, entry.title);
    seen.add(id);
    const result = byKey.get(id);
    const issue = input.expectations[entry.cell]?.[entry.title];
    const isBlocked = blocked.has(entry.cell) && entry.title !== UP_TITLE;
    return {
      cell: entry.cell,
      title: entry.title,
      leg: entry.leg,
      verdict: verdictFor(result, issue, isBlocked),
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
