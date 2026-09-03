import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import type { Reporter, TestCase } from "vitest/node";
import { expectationsFor } from "./expectations";
import { currentRunIdentity } from "./identity";
import { outputRoot } from "./paths";
import { planTests } from "./plan";
import { reconcile, type TestOutcome, type TestResult } from "./reconcile";
import { specForTarget } from "./spec";
import { failureReport, summaryTable } from "./summary";
import { selectedTarget } from "./targets";

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

export default class JourneyReporter implements Reporter {
  private readonly results: TestResult[] = [];

  onTestCaseResult(testCase: TestCase) {
    const parent = testCase.parent;
    if (parent.type !== "suite") {
      return;
    }
    this.results.push({
      cell: parent.name,
      title: testCase.name,
      outcome: outcomeOf(testCase),
      error: errorOf(testCase),
    });
  }

  async onTestRunEnd() {
    const target = selectedTarget();
    const runId = currentRunIdentity();
    const chosen = process.env.OCEL_EXAMPLES?.split(",").map((name) => name.trim());
    const examples = specForTarget(target.name).filter(
      (row) => chosen === undefined || chosen.includes(row.name),
    );
    let environment: Awaited<ReturnType<typeof target.guard>>;
    try {
      environment = await target.guard();
    } catch (error) {
      process.stderr.write(`\nthe journey account cannot be read: ${String(error)}\n`);
      process.exitCode = 1;
      return;
    }

    const report = reconcile({
      planned: planTests(examples, target.legs),
      results: this.results,
      expectations: expectationsFor(environment),
    });

    const table = summaryTable(report, { target: target.name, environment, runId });
    const dir = path.join(outputRoot, runId, target.name);
    await mkdir(dir, { recursive: true });
    await writeFile(path.join(dir, "summary.md"), table, "utf8");

    const stepSummary = process.env.GITHUB_STEP_SUMMARY;
    if (stepSummary) {
      await writeFile(stepSummary, table, { encoding: "utf8", flag: "a" });
    }

    if (report.failed) {
      process.stderr.write(`\nthe journey account does not reconcile:\n${failureReport(report)}\n`);
      process.exitCode = 1;
    }
  }
}
