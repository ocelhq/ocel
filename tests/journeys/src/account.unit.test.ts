import { describe, expect, it } from "bun:test";
import { accountOf, plannedFrom, type Run, unhandledFrom } from "./account";
import type { RecordedRow } from "./ledger";
import type { PlannedTest } from "./plan";
import { selectionFor } from "./selection";
import { targetNamed } from "./targets";

function run(over: Partial<Run> = {}): Run {
  return { exitCode: 0, signal: null, ...over };
}

function row(over: Partial<RecordedRow> = {}): RecordedRow {
  return {
    cell: "sdk/node/web",
    title: "up",
    outcome: "passed",
    startTime: 1_000,
    duration: 500,
    ...over,
  };
}

describe("the errors the suite leaves outside its recorded tests", () => {
  it("says nothing when the suite exited clean", () => {
    expect(unhandledFrom(run(), [row()])).toEqual([]);
  });

  it("says nothing when the suite exited 1 with a failing test of its own", () => {
    expect(unhandledFrom(run({ exitCode: 1 }), [row({ outcome: "failed" })])).toEqual([]);
  });

  it("names an exit of 1 that no failing test accounts for", () => {
    expect(unhandledFrom(run({ exitCode: 1 }), [row()])).toEqual([
      "bun test exited 1 without a failing test",
    ]);
  });

  it("names a signal, however the tests went", () => {
    expect(
      unhandledFrom(run({ exitCode: null, signal: "SIGKILL" }), [row({ outcome: "failed" })]),
    ).toEqual(["bun test exited SIGKILL without a failing test"]);
  });

  it("names an exit code that is no test result", () => {
    expect(unhandledFrom(run({ exitCode: 2 }), [])).toEqual([
      "bun test exited 2 without a failing test",
    ]);
  });
});

describe("what a lane plans from the environment its children read", () => {
  it("plans only the fixtures the environment names", () => {
    const aws = targetNamed("aws");
    const planned = plannedFrom(
      aws,
      selectionFor(aws, "aws", {
        OCEL_JOURNEY_FIXTURES: "deploy/node,sdk/next",
        OCEL_JOURNEY_SKIPS: "run",
      }),
    );
    const fixtures = new Set(planned.map((entry) => entry.fixture));
    expect([...fixtures].sort()).toEqual(["deploy/node", "sdk/next"]);
  });
});

describe("the account a run settles from its rows", () => {
  const planned: PlannedTest[] = [
    {
      cell: "sdk/node/web",
      fixture: "sdk/node",
      app: "web",
      variant: "base",
      title: "up",
      leg: "up",
    },
    {
      cell: "sdk/node/web",
      fixture: "sdk/node",
      app: "web",
      variant: "base",
      title: "destroy",
      leg: "destroy",
    },
  ];
  const meta = { target: "dev", environment: "dev", runId: "local-unit", leftOut: [] };

  it("reconciles the recorded rows and holds a crash as an unhandled error", () => {
    const account = accountOf({
      rows: [row({ title: "up" }), row({ title: "destroy", startTime: 1_600 })],
      run: run({ exitCode: null, signal: "SIGKILL" }),
      runStart: 0,
      runEnd: 4_000,
      workers: 2,
      planned,
      expectations: {},
      meta,
    });
    expect(account.report.failed).toBe(false);
    expect(account.verdict.exitCode).toBe(1);
    expect(account.verdict.report).toContain(
      "UNHANDLED — bun test exited SIGKILL without a failing test",
    );
  });

  it("holds a failing test as the failure, with nothing unhandled beside it", () => {
    const account = accountOf({
      rows: [
        row({ title: "up", outcome: "failed", error: "the console never answered" }),
        row({ title: "destroy", startTime: 1_600 }),
      ],
      run: run({ exitCode: 1 }),
      runStart: 0,
      runEnd: 4_000,
      workers: 2,
      planned,
      expectations: {},
      meta,
    });
    expect(account.verdict.exitCode).toBe(1);
    expect(account.verdict.report).toContain("NEW RED — sdk/node/web › up");
    expect(account.verdict.report).not.toContain("UNHANDLED");
  });

  it("spans each cell's rows for its own wall clock in the timeline", () => {
    const account = accountOf({
      rows: [
        row({ title: "up", startTime: 1_000, duration: 3_000 }),
        row({ cell: "sdk/node-container/web", title: "up", startTime: 2_000, duration: 9_000 }),
      ],
      run: run(),
      runStart: 0,
      runEnd: 12_000,
      workers: 2,
      planned,
      expectations: {},
      meta,
    });
    expect(account.timeline.cells.map((entry) => [entry.cell, entry.file])).toEqual([
      ["sdk/node-container", 9],
      ["sdk/node", 3],
    ]);
    expect(account.timeline.files).toBe(12);
  });
});
