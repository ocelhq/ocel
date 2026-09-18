import { afterAll, beforeAll, describe, it } from "bun:test";
import { readFileSync } from "node:fs";
import { evidence } from "../evidence";
import { currentRunIdentity } from "../identity";
import { phasesDriven, stepsPlanned } from "../lifecycle";
import { live } from "../live";
import { fixtures } from "../matrix/fixtures";
import { evidenceDir } from "../paths";
import { cellKey, cellNamed, type Plan } from "../plan";
import { readPrepareFailure } from "../prepare";
import { targetNamed } from "../targets";
import { CellRun } from "./cellRun";
import { ledgerFor } from "./ledger";

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function describeCell(planFile: string, name: string) {
  const planned = JSON.parse(readFileSync(planFile, "utf8")) as Plan;
  const plannedCell = planned.cells.find((one) => one.name === name);
  if (!plannedCell) {
    throw new Error(`${planFile} plans no cell named ${name}`);
  }
  const target = targetNamed(planned.target);
  const phases = phasesDriven(target, plannedCell.phases);
  const cell = cellNamed(fixtures, target.name, name);
  const steps = stepsPlanned(cell, plannedCell);
  const runId = currentRunIdentity();
  const prepareFailure = readPrepareFailure(runId, target.name);
  const run = new CellRun({
    cell,
    target,
    runId,
    keep: planned.keep,
    evidence: evidence(evidenceDir(runId, target.name, name)),
    ...(prepareFailure === undefined ? {} : { prepareFailure }),
  });

  const write = ledgerFor(runId, target.name, name);
  const say = live(name);
  const timeout = target.stepTimeoutMs;

  describe(name, () => {
    beforeAll(() => run.prepareProcess(), { timeout });

    afterAll(() => run.finish(phases), { timeout });

    for (const step of steps) {
      const key = cellKey(name, step.app);
      describe(key, () => {
        it(
          step.title,
          async () => {
            const startTime = Date.now();
            say(`▶ ${step.title}`);
            try {
              await step.run(run);
              say(`✓ ${step.title} (${((Date.now() - startTime) / 1000).toFixed(1)}s)`);
              write({
                cell: key,
                title: step.title,
                outcome: "passed",
                startTime,
                duration: Date.now() - startTime,
              });
            } catch (error) {
              say(`✗ ${step.title}: ${messageOf(error).split("\n")[0]}`);
              write({
                cell: key,
                title: step.title,
                outcome: "failed",
                error: messageOf(error),
                startTime,
                duration: Date.now() - startTime,
              });
              throw error;
            }
          },
          timeout,
        );
      });
    }
  });
}
