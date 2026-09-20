import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { currentRunIdentity } from "../identity";
import { laneDir } from "../paths";
import { type ExpectedFailures, type Plan, type PlannedTest, testsOf } from "../plan";
import { readPrepared } from "../prepare";
import { readResults, type StepResult } from "../run/results";
import type { Target } from "../targets/types";
import { type Report, reconcile, type TestResult } from "./reconcile";
import { journeyVerdict, type SummaryMeta, summaryTable } from "./summary";
import {
  type Timeline,
  type TimelineModule,
  type TimelineTest,
  timelineOf,
  timingTable,
} from "./timeline";

export type SuiteExit = { exitCode: number | null; signal: string | null };

export type TimingInput = {
  results: StepResult[];
  prepareMs?: number;
  runStart: number;
  runEnd: number;
  workers: number;
  planned: PlannedTest[];
};

export type JourneyReportInput = TimingInput & {
  exit: SuiteExit;
  expectedFailures: ExpectedFailures;
  meta: SummaryMeta;
  prepareFailure?: string;
};

export type Timed = { tests: TimelineTest[]; modules: TimelineModule[]; timeline: Timeline };

export type JourneyReport = Timed & {
  report: Report;
  summary: string;
  timing: string;
  verdict: { exitCode: number; report: string };
};

function cellOf(key: string): string {
  return key.split("/").slice(0, -1).join("/");
}

function key(cell: string, title: string): string {
  return JSON.stringify([cell, title]);
}

export function unhandledFrom(exit: SuiteExit, results: StepResult[]): string[] {
  if (exit.signal === null && (exit.exitCode === 0 || exit.exitCode === 1)) {
    if (exit.exitCode === 0 || results.some((result) => result.outcome === "failed")) {
      return [];
    }
  }
  return [`bun test exited ${exit.signal ?? exit.exitCode} without a failing test`];
}

function modulesFrom(results: StepResult[]): TimelineModule[] {
  const spans = new Map<string, { from: number; to: number }>();
  for (const result of results) {
    const cell = cellOf(result.cell);
    const held = spans.get(cell);
    const from = Math.min(held?.from ?? result.startTime, result.startTime);
    const to = Math.max(held?.to ?? 0, result.startTime + result.duration);
    spans.set(cell, { from, to });
  }
  return [...spans.entries()].map(([cell, span]) => ({
    cell,
    duration: span.to - span.from,
  }));
}

export function timelineFrom(input: TimingInput): Timed {
  const phaseByKey = new Map(
    input.planned.map((entry) => [key(entry.cell, entry.title), entry.phase]),
  );
  const tests: TimelineTest[] = input.results.map((result) => ({
    cell: cellOf(result.cell),
    phase: phaseByKey.get(key(result.cell, result.title)),
    title: result.title,
    startTime: result.startTime,
    duration: result.duration,
  }));
  const modules = modulesFrom(input.results);
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

export function journeyReportOf(input: JourneyReportInput): JourneyReport {
  const timed = timelineFrom(input);
  const outcomes: TestResult[] = input.results.map((result) => ({
    cell: result.cell,
    title: result.title,
    outcome: result.outcome,
    ...(result.error === undefined ? {} : { error: result.error }),
  }));
  const report = reconcile({
    planned: input.planned,
    results: outcomes,
    expectedFailures: input.expectedFailures,
    ...(input.prepareFailure === undefined ? {} : { prepareFailure: input.prepareFailure }),
  });
  return {
    ...timed,
    report,
    summary: summaryTable(report, input.meta),
    timing: timingTable(timed.timeline, { target: input.meta.target, runId: input.meta.runId }),
    verdict: journeyVerdict(report, unhandledFrom(input.exit, input.results)),
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

export async function writeReport(input: {
  target: Target;
  plan: Plan;
  exit: SuiteExit;
  runStart: number;
  runEnd: number;
  workers: number;
}): Promise<{ exitCode: number; report: string }> {
  const runId = currentRunIdentity();
  const dir = laneDir(runId, input.target.name);
  await mkdir(dir, { recursive: true });

  const planned = testsOf(input.plan);
  const prepared = readPrepared(runId, input.target.name);
  const shared: TimingInput = {
    results: await readResults(runId, input.target.name),
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

  const account = journeyReportOf({
    ...shared,
    exit: input.exit,
    expectedFailures: input.plan.expectedFailures,
    ...(prepared?.failures.lane === undefined ? {} : { prepareFailure: prepared.failures.lane }),
    meta: {
      target: input.target.name,
      lane: input.plan.lane,
      runId,
      skipped: input.plan.skipped,
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
