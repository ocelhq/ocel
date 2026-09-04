import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import type { Reporter, TestCase, TestModule } from "vitest/node";
import { expectationsFor } from "./expectations";
import { currentRunIdentity } from "./identity";
import { exampleOf } from "./order";
import { laneDir, laneEdge, prepareFile, verdictFile } from "./paths";
import { planTests } from "./plan";
import { reconcile, type TestOutcome, type TestResult } from "./reconcile";
import { specForTarget } from "./spec";
import { journeyVerdict, summaryTable } from "./summary";
import { laneWorkers, selectedTarget } from "./targets";
import { timelineOf, type TimelineModule, type TimelineTest, timingTable } from "./timeline";

type Timing = { cell: string; title: string; startTime: number; duration: number };

function outcomeOf(testCase: TestCase): TestOutcome {
  const mode = testCase.options.mode;
  if (mode === "only" || mode === "todo" || mode === "skip") {
    return mode === "skip" ? "skipped" : mode;
  }
  const state = testCase.result().state;
  if (state === "passed") {
    return "passed";
  }
  if (state === "skipped") {
    return "skipped";
  }
  return "failed";
}

function errorOf(testCase: TestCase): string | undefined {
  return testCase.result().errors?.[0]?.message;
}

function cellOf(testCase: TestCase): string {
  const parent = testCase.parent;
  if (parent.type === "suite") {
    return parent.name;
  }
  return `top level · ${path.basename(parent.moduleId)}`;
}

function key(cell: string, title: string): string {
  return JSON.stringify([cell, title]);
}

async function writeVerdict(runId: string, target: string, exitCode: number, report: string) {
  const file = verdictFile(runId, target);
  await mkdir(path.dirname(file), { recursive: true });
  const nonce = process.env.OCEL_JOURNEY_VERDICT_NONCE ?? "";
  await writeFile(file, `${JSON.stringify({ nonce, exitCode, report }, null, 2)}\n`, "utf8");
}

async function prepareMs(runId: string, target: string): Promise<number | undefined> {
  try {
    const read = JSON.parse(await readFile(prepareFile(runId, target), "utf8")) as { ms?: number };
    return typeof read.ms === "number" ? read.ms : undefined;
  } catch {
    return undefined;
  }
}

export default class JourneyReporter implements Reporter {
  private readonly results: TestResult[] = [];
  private readonly timings: Timing[] = [];
  private readonly modules: TimelineModule[] = [];
  private runStart = Date.now();
  private runEnd = Date.now();

  onTestRunStart() {
    this.runStart = Date.now();
  }

  onTestCaseResult(testCase: TestCase) {
    const cell = cellOf(testCase);
    this.results.push({
      cell,
      title: testCase.name,
      outcome: outcomeOf(testCase),
      error: errorOf(testCase),
    });
    const diagnostic = testCase.diagnostic();
    if (diagnostic) {
      this.timings.push({
        cell,
        title: testCase.name,
        startTime: diagnostic.startTime,
        duration: diagnostic.duration,
      });
    }
  }

  onTestModuleEnd(module: TestModule) {
    this.modules.push({
      example: exampleOf(path.basename(module.moduleId)),
      duration: module.diagnostic().duration,
    });
  }

  async onTestRunEnd(
    _modules: ReadonlyArray<unknown>,
    unhandledErrors: ReadonlyArray<{ message?: string; stack?: string }>,
  ) {
    this.runEnd = Date.now();
    const target = selectedTarget();
    const runId = currentRunIdentity();
    const chosen = process.env.OCEL_EXAMPLES?.split(",").map((name) => name.trim());
    const examples = specForTarget(target.name).filter(
      (row) => chosen === undefined || chosen.includes(row.name),
    );
    const dir = laneDir(runId, target.name);
    await mkdir(dir, { recursive: true });
    const planned = planTests(examples, target.legs, target);
    const timing = await this.writeTiming(dir, runId, target.name, planned);

    let environment: Awaited<ReturnType<typeof target.guard>>;
    try {
      environment = await target.guard();
    } catch (error) {
      const said = `the journey account cannot be read: ${String(error)}`;
      process.stderr.write(`\n${said}\n`);
      await writeVerdict(runId, target.name, 1, said);
      process.exitCode = 1;
      return;
    }

    const report = reconcile({
      planned,
      results: this.results,
      expectations: expectationsFor(environment),
    });

    const leftOut = (process.env.OCEL_JOURNEY_LEFT_OUT ?? "")
      .split(",")
      .map((name) => name.trim())
      .filter((name) => name !== "");
    const table = summaryTable(report, { target: target.name, environment, runId, leftOut });
    await writeFile(path.join(dir, "summary.md"), table, "utf8");

    const stepSummary = process.env.GITHUB_STEP_SUMMARY;
    if (stepSummary) {
      await writeFile(stepSummary, `${table}\n${timing}`, { encoding: "utf8", flag: "a" });
    }

    const verdict = journeyVerdict(
      report,
      unhandledErrors.map((error) => error.message ?? error.stack ?? String(error)),
    );
    await writeVerdict(runId, target.name, verdict.exitCode, verdict.report);
    if (verdict.exitCode !== 0) {
      process.stderr.write(`\nthe journey account does not reconcile:\n${verdict.report}\n`);
      process.exitCode = 1;
    }
  }

  private async writeTiming(
    dir: string,
    runId: string,
    target: string,
    planned: ReturnType<typeof planTests>,
  ): Promise<string> {
    const legByKey = new Map(planned.map((entry) => [key(entry.cell, entry.title), entry.leg]));
    const tests: TimelineTest[] = this.timings.map((row) => ({
      example: row.cell.split("/")[0],
      leg: legByKey.get(key(row.cell, row.title)),
      title: row.title,
      startTime: row.startTime,
      duration: row.duration,
    }));
    const timeline = timelineOf({
      runStart: this.runStart,
      runEnd: this.runEnd,
      workers: laneWorkers(selectedTarget()),
      tests,
      modules: this.modules,
      prepareMs: await prepareMs(runId, target),
    });
    const table = timingTable(timeline, { target, edge: laneEdge(target), runId });
    await writeFile(
      path.join(dir, "timeline.json"),
      `${JSON.stringify({ runStart: this.runStart, runEnd: this.runEnd, timeline, tests, modules: this.modules }, null, 2)}\n`,
      "utf8",
    );
    await writeFile(path.join(dir, "timing.md"), table, "utf8");
    return table;
  }
}
