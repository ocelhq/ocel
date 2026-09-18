import { describe, expect, it } from "bun:test";
import type { PlannedTest } from "../plan";
import type { StepResult } from "../run/results";
import { journeyReportOf, type SuiteExit, unhandledFrom } from "./index";

function exit(over: Partial<SuiteExit> = {}): SuiteExit {
  return { exitCode: 0, signal: null, ...over };
}

function result(over: Partial<StepResult> = {}): StepResult {
  return {
    cell: "sdk/node/web",
    title: "deploy",
    outcome: "passed",
    startTime: 1_000,
    duration: 500,
    ...over,
  };
}

describe("the errors the suite leaves outside its recorded tests", () => {
  it("says nothing when the suite exited clean", () => {
    expect(unhandledFrom(exit(), [result()])).toEqual([]);
  });

  it("says nothing when the suite exited 1 with a failing test of its own", () => {
    expect(unhandledFrom(exit({ exitCode: 1 }), [result({ outcome: "failed" })])).toEqual([]);
  });

  it("names an exit of 1 that no failing test accounts for", () => {
    expect(unhandledFrom(exit({ exitCode: 1 }), [result()])).toEqual([
      "bun test exited 1 without a failing test",
    ]);
  });

  it("names a signal, however the tests went", () => {
    expect(
      unhandledFrom(exit({ exitCode: null, signal: "SIGKILL" }), [result({ outcome: "failed" })]),
    ).toEqual(["bun test exited SIGKILL without a failing test"]);
  });

  it("names an exit code that is no test result", () => {
    expect(unhandledFrom(exit({ exitCode: 2 }), [])).toEqual([
      "bun test exited 2 without a failing test",
    ]);
  });
});

describe("the report a run writes from its step results", () => {
  const planned: PlannedTest[] = [
    {
      cell: "sdk/node/web",
      title: "deploy",
      phase: "deploy",
    },
    {
      cell: "sdk/node/web",
      title: "destroy",
      phase: "destroy",
    },
  ];
  const meta = { target: "dev", lane: "dev", runId: "local-unit" };

  it("reconciles the recorded results and holds a crash as an unhandled error", () => {
    const account = journeyReportOf({
      results: [result({ title: "deploy" }), result({ title: "destroy", startTime: 1_600 })],
      exit: exit({ exitCode: null, signal: "SIGKILL" }),
      runStart: 0,
      runEnd: 4_000,
      workers: 2,
      planned,
      expectedFailures: {},
      meta,
    });
    expect(account.report.failed).toBe(false);
    expect(account.verdict.exitCode).toBe(1);
    expect(account.verdict.report).toContain(
      "UNHANDLED — bun test exited SIGKILL without a failing test",
    );
  });

  it("holds a failing test as the failure, with nothing unhandled beside it", () => {
    const account = journeyReportOf({
      results: [
        result({ title: "deploy", outcome: "failed", error: "the console never answered" }),
        result({ title: "destroy", startTime: 1_600 }),
      ],
      exit: exit({ exitCode: 1 }),
      runStart: 0,
      runEnd: 4_000,
      workers: 2,
      planned,
      expectedFailures: {},
      meta,
    });
    expect(account.verdict.exitCode).toBe(1);
    expect(account.verdict.report).toContain("NEW RED — sdk/node/web › deploy");
    expect(account.verdict.report).not.toContain("UNHANDLED");
  });

  it("spans each cell's results for its own wall clock in the timeline", () => {
    const account = journeyReportOf({
      results: [
        result({ title: "deploy", startTime: 1_000, duration: 3_000 }),
        result({
          cell: "sdk/node-container/web",
          title: "deploy",
          startTime: 2_000,
          duration: 9_000,
        }),
      ],
      exit: exit(),
      runStart: 0,
      runEnd: 12_000,
      workers: 2,
      planned,
      expectedFailures: {},
      meta,
    });
    expect(account.timeline.cells.map((entry) => [entry.cell, entry.file])).toEqual([
      ["sdk/node-container", 9],
      ["sdk/node", 3],
    ]);
    expect(account.timeline.files).toBe(12);
  });
});
