import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { cellOf, type ExpectationEnvironment, expectationsFor } from "./expectations";
import type { Expectations } from "./expectations/types";
import { currentRunIdentity } from "./identity";
import { readRows, type RecordedRow } from "./ledger";
import { laneDir } from "./paths";
import { type PlannedTest, planTests } from "./plan";
import { readPrepared } from "./prepare";
import { reconcile, type Report, type TestResult } from "./reconcile";
import { type Selection, selectionFor } from "./selection";
import { journeyVerdict, type SummaryMeta, summaryTable } from "./summary";
import type { Target } from "./targets/types";
import {
  timelineOf,
  type Timeline,
  type TimelineModule,
  type TimelineTest,
  timingTable,
} from "./timeline";

export type Run = { exitCode: number | null; signal: string | null };

export type TimingInput = {
  rows: RecordedRow[];
  prepareMs?: number;
  runStart: number;
  runEnd: number;
  workers: number;
  planned: PlannedTest[];
};

export type AccountInput = TimingInput & {
  run: Run;
  expectations: Expectations;
  meta: SummaryMeta;
};

export type Timed = { tests: TimelineTest[]; modules: TimelineModule[]; timeline: Timeline };

export type Account = Timed & {
  report: Report;
  summary: string;
  timing: string;
  verdict: { exitCode: number; report: string };
};

export function plannedFrom(target: Target, selection: Selection): PlannedTest[] {
  return planTests(selection.cells, target.legs);
}

function key(cell: string, title: string): string {
  return JSON.stringify([cell, title]);
}

export function unhandledFrom(run: Run, rows: RecordedRow[]): string[] {
  if (run.signal === null && (run.exitCode === 0 || run.exitCode === 1)) {
    if (run.exitCode === 0 || rows.some((row) => row.outcome === "failed")) {
      return [];
    }
  }
  return [`bun test exited ${run.signal ?? run.exitCode} without a failing test`];
}

function modulesFrom(rows: RecordedRow[]): TimelineModule[] {
  const spans = new Map<string, { from: number; to: number }>();
  for (const row of rows) {
    const cell = cellOf(row.cell);
    const held = spans.get(cell);
    const from = Math.min(held?.from ?? row.startTime, row.startTime);
    const to = Math.max(held?.to ?? 0, row.startTime + row.duration);
    spans.set(cell, { from, to });
  }
  return [...spans.entries()].map(([cell, span]) => ({
    cell,
    duration: span.to - span.from,
  }));
}

export function timelineFrom(input: TimingInput): Timed {
  const legByKey = new Map(input.planned.map((entry) => [key(entry.cell, entry.title), entry.leg]));
  const tests: TimelineTest[] = input.rows.map((row) => ({
    cell: cellOf(row.cell),
    leg: legByKey.get(key(row.cell, row.title)),
    title: row.title,
    startTime: row.startTime,
    duration: row.duration,
  }));
  const modules = modulesFrom(input.rows);
  return {
    tests,
    modules,
    timeline: timelineOf({
      runStart: input.runStart,
      runEnd: input.runEnd,
      workers: input.workers,
      tests,
      modules,
      ...(input.prepareMs === undefined ? {} : { prepareMs: input.prepareMs }),
    }),
  };
}

export function accountOf(input: AccountInput): Account {
  const timed = timelineFrom(input);
  const results: TestResult[] = input.rows.map((row) => ({
    cell: row.cell,
    title: row.title,
    outcome: row.outcome,
    ...(row.error === undefined ? {} : { error: row.error }),
  }));
  const report = reconcile({
    planned: input.planned,
    results,
    expectations: input.expectations,
  });
  return {
    ...timed,
    report,
    summary: summaryTable(report, input.meta),
    timing: timingTable(timed.timeline, { target: input.meta.target, runId: input.meta.runId }),
    verdict: journeyVerdict(report, unhandledFrom(input.run, input.rows)),
  };
}

async function writeTiming(
  dir: string,
  timed: Timed,
  meta: { target: string; runId: string; runStart: number; runEnd: number },
) {
  await writeFile(path.join(dir, "timing.md"), timingTable(timed.timeline, meta), "utf8");
  await writeFile(
    path.join(dir, "timeline.json"),
    `${JSON.stringify(
      {
        runStart: meta.runStart,
        runEnd: meta.runEnd,
        timeline: timed.timeline,
        tests: timed.tests,
        modules: timed.modules,
      },
      null,
      2,
    )}\n`,
    "utf8",
  );
}

export async function settleAccount(input: {
  target: Target;
  environment: ExpectationEnvironment;
  env: NodeJS.ProcessEnv;
  run: Run;
  runStart: number;
  runEnd: number;
  workers: number;
}): Promise<{ exitCode: number; report: string }> {
  const runId = currentRunIdentity();
  const dir = laneDir(runId, input.target.name);
  await mkdir(dir, { recursive: true });

  const selection = selectionFor(input.target, input.environment, input.env);
  const planned = plannedFrom(input.target, selection);
  const prepared = readPrepared(runId, input.target.name);
  const shared: TimingInput = {
    rows: await readRows(runId, input.target.name),
    ...(prepared === undefined ? {} : { prepareMs: prepared.ms }),
    runStart: input.runStart,
    runEnd: input.runEnd,
    workers: input.workers,
    planned,
  };
  await writeTiming(dir, timelineFrom(shared), {
    target: input.target.name,
    runId,
    runStart: input.runStart,
    runEnd: input.runEnd,
  });

  const leftOut = (input.env.OCEL_JOURNEY_LEFT_OUT ?? "")
    .split(",")
    .map((name) => name.trim())
    .filter((name) => name !== "");
  const account = accountOf({
    ...shared,
    run: input.run,
    expectations: expectationsFor(input.environment),
    meta: {
      target: input.target.name,
      environment: input.environment,
      runId,
      leftOut,
      skipped: selection.skipped,
    },
  });

  await writeFile(path.join(dir, "summary.md"), account.summary, "utf8");
  const stepSummary = process.env.GITHUB_STEP_SUMMARY;
  if (stepSummary) {
    await writeFile(stepSummary, `${account.summary}\n${account.timing}`, {
      encoding: "utf8",
      flag: "a",
    });
  }
  return account.verdict;
}
