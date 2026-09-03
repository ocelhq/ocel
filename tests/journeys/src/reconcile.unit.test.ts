import { describe, expect, it } from "vitest";
import { UP_TITLE } from "./plan";
import { exitCodeFor, reconcile, type TestOutcome, type TestResult } from "./reconcile";

const ISSUE = "https://github.com/ocelhq/ocel/issues/851";

const planned = [
  { cell: "express/web", title: UP_TITLE, leg: "up" as const },
  { cell: "express/web", title: "GET /health answers", leg: "contract" as const },
  { cell: "express/web", title: "destroy", leg: "destroy" as const },
];

function result(title: string, outcome: TestOutcome): TestResult {
  return { cell: "express/web", title, outcome };
}

function allRan(outcome: TestOutcome = "passed"): TestResult[] {
  return planned.map((entry) => result(entry.title, outcome));
}

describe("reconciliation", () => {
  it("is green when every planned test passed and nothing is listed", () => {
    const report = reconcile({ planned, results: allRan(), expectations: {} });
    expect(report.failed).toBe(false);
    expect(report.rows.every((row) => row.verdict === "ok")).toBe(true);
  });

  it("stays green when a listed test fails", () => {
    const results = allRan();
    results[1] = result("GET /health answers", "failed");
    const report = reconcile({
      planned,
      results,
      expectations: { "express/web": { "GET /health answers": ISSUE } },
    });
    expect(report.failed).toBe(false);
    const row = report.rows.find((entry) => entry.title === "GET /health answers");
    expect(row).toMatchObject({ verdict: "expected-failure", issue: ISSUE });
  });

  it("fails when a listed test passes", () => {
    const report = reconcile({
      planned,
      results: allRan(),
      expectations: { "express/web": { "GET /health answers": ISSUE } },
    });
    expect(report.failed).toBe(true);
    expect(report.failures.map((row) => row.verdict)).toContain("listed-and-passed");
  });

  it("fails when an unlisted test fails", () => {
    const results = allRan();
    results[1] = result("GET /health answers", "failed");
    const report = reconcile({ planned, results, expectations: {} });
    expect(report.failed).toBe(true);
    expect(report.failures.map((row) => row.verdict)).toContain("unexpected-failure");
  });

  it("fails a disabled test even when it is listed", () => {
    const results = allRan();
    results[1] = result("GET /health answers", "skipped");
    const report = reconcile({
      planned,
      results,
      expectations: { "express/web": { "GET /health answers": ISSUE } },
    });
    expect(report.failed).toBe(true);
  });

  it("fails when a planned test never ran", () => {
    const results = allRan().filter((entry) => entry.title !== "GET /health answers");
    const report = reconcile({ planned, results, expectations: {} });
    expect(report.failed).toBe(true);
    expect(report.failures.map((row) => row.verdict)).toContain("never-ran");
  });

  it("fails on a result nothing planned", () => {
    const results = [...allRan(), result("a stray test", "passed")];
    const report = reconcile({ planned, results, expectations: {} });
    expect(report.failed).toBe(true);
    expect(report.failures.map((row) => row.verdict)).toContain("unplanned");
  });

  it("blocks the rest of a cell whose listed up failed", () => {
    const report = reconcile({
      planned,
      results: [result(UP_TITLE, "failed")],
      expectations: { "express/web": { [UP_TITLE]: ISSUE } },
    });
    expect(report.failed).toBe(false);
    expect(report.rows.map((row) => row.verdict)).toEqual([
      "expected-failure",
      "blocked",
      "blocked",
    ]);
  });

  it("blocks the rows that failed behind a listed up, whatever order they arrive in", () => {
    const results: TestResult[] = [
      result("GET /health answers", "failed"),
      result("destroy", "failed"),
      result(UP_TITLE, "failed"),
    ];
    const expectations = { "express/web": { [UP_TITLE]: ISSUE } };
    for (const ordered of [results, [...results].reverse()]) {
      const report = reconcile({ planned, results: ordered, expectations });
      expect(report.rows.map((row) => row.verdict)).toEqual([
        "expected-failure",
        "blocked",
        "blocked",
      ]);
      expect(exitCodeFor(report.rows.map((row) => row.verdict))).toBe(0);
    }
  });

  it("blocks the rows that failed behind an unlisted up, and fails on the up alone", () => {
    const report = reconcile({
      planned,
      results: [
        result(UP_TITLE, "failed"),
        result("GET /health answers", "failed"),
        result("destroy", "failed"),
      ],
      expectations: {},
    });
    expect(report.rows.map((row) => row.verdict)).toEqual([
      "unexpected-failure",
      "blocked",
      "blocked",
    ]);
    expect(exitCodeFor(report.rows.map((row) => row.verdict))).toBe(1);
  });

  it.each(["skipped", "todo", "only"] as const)(
    "fails on a %s row wherever it appears, including behind a listed up",
    (outcome) => {
      const cameUp = allRan();
      cameUp[1] = result("GET /health answers", outcome);
      const clean = reconcile({ planned, results: cameUp, expectations: {} });
      expect(clean.rows.map((row) => row.verdict)).toEqual(["ok", "disabled", "ok"]);
      expect(exitCodeFor(clean.rows.map((row) => row.verdict))).toBe(1);

      const blocked = reconcile({
        planned,
        results: [result(UP_TITLE, "failed"), result("GET /health answers", outcome)],
        expectations: { "express/web": { [UP_TITLE]: ISSUE } },
      });
      expect(blocked.failures.map((row) => row.verdict)).toEqual(["disabled"]);
      expect(exitCodeFor(blocked.rows.map((row) => row.verdict))).toBe(1);
    },
  );

  it("fails a cell whose up failed unlisted", () => {
    const report = reconcile({
      planned,
      results: [result(UP_TITLE, "failed")],
      expectations: {},
    });
    expect(report.failed).toBe(true);
    expect(report.failures.map((row) => row.verdict)).toEqual([
      "unexpected-failure",
      "never-ran",
      "never-ran",
    ]);
  });
});

describe("exit code", () => {
  it("is zero for a run that only passed or failed as listed", () => {
    expect(exitCodeFor(["ok", "expected-failure", "blocked"])).toBe(0);
  });

  it("is one for every verdict the account refuses", () => {
    for (const verdict of [
      "unexpected-failure",
      "listed-and-passed",
      "never-ran",
      "disabled",
      "unplanned",
    ] as const) {
      expect(exitCodeFor(["ok", verdict])).toBe(1);
    }
  });
});
