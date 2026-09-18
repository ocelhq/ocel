import { afterAll, beforeAll, describe, it } from "bun:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { INITIAL_GREETING, REDEPLOY_GREETING, secretGuarded } from "../contract";
import { evidence } from "../evidence";
import { currentRunIdentity, projectSlug } from "../identity";
import { type CellRun, legsDriven, stepsPlanned } from "../lifecycle";
import { live } from "../live";
import { fixtures } from "../matrix/fixtures";
import type { Leg } from "../matrix/types";
import { evidenceDir, fixtureDir } from "../paths";
import { cellKey, cellNamed, type Plan } from "../plan";
import { readPrepareFailure } from "../prepare";
import { targetNamed } from "../targets";
import { namespaceOfSlug } from "../targets/aws/namespace";
import type { CellContext, Deployment } from "../targets/types";
import { ledgerFor } from "./ledger";

function once<T>(work: () => Promise<T>): () => Promise<T> {
  let started: Promise<T> | undefined;
  return () => {
    started ??= work();
    return started;
  };
}

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
  const legs = legsDriven(target, plannedCell.legs);
  const found = cellNamed(fixtures, target.name, name);
  const steps = stepsPlanned(found, plannedCell);
  const { fixture, variant } = found;
  const runId = currentRunIdentity();
  const slug = projectSlug(name, runId);
  const cell: CellContext = {
    fixture,
    name,
    ...(variant === undefined ? {} : { variant }),
    dir: fixtureDir(fixture.name),
    slug,
    runId,
    evidence: evidence(evidenceDir(runId, target.name, name)),
  };

  const write = ledgerFor(runId, target.name, name);
  const say = live(name);
  const timeout = target.legTimeoutMs;
  const ladder = fixture.ladder;

  let deployment: Deployment | undefined;
  let greeting = INITIAL_GREETING;
  const notes = new Map<string, string>();

  const prepareFailure = readPrepareFailure(runId, target.name);
  let setupFailure: { error: unknown } | undefined = prepareFailure
    ? { error: new Error(prepareFailure) }
    : undefined;

  const bringUp = once(async () => {
    if (setupFailure) {
      throw setupFailure.error;
    }
    deployment = await target.up(cell);
  });
  const tearDown = once(() => target.destroy(cell));
  const beforeUp = once(async () => {
    await ladder?.beforeUp?.(cell);
  });
  const afterDestroy = once(async () => {
    if (!planned.keep) {
      await ladder?.afterDestroy?.(cell);
    }
  });
  const redeployed = once(async () => {
    assert.ok(target.redeploy);
    deployment = await target.redeploy(cell, REDEPLOY_GREETING);
    greeting = REDEPLOY_GREETING;
  });
  const rolledBack = once(async () => {
    assert.ok(target.rollback);
    deployment = await target.rollback(cell, INITIAL_GREETING);
    greeting = INITIAL_GREETING;
  });

  const run: CellRun = {
    cell,
    beforeUp,
    up: bringUp,
    redeploy: async () => {
      await bringUp();
      await redeployed();
    },
    rollback: async () => {
      await bringUp();
      await rolledBack();
    },
    destroy: async () => {
      await tearDown();
      assert.ok(
        !(await target.stands(slug)),
        `${slug} still exists on ${target.name} after destroy`,
      );
    },
    afterDestroy,
    live: (app: string, leg: Leg) => {
      assert.ok(deployment, "the contract ran before the cell came up");
      return {
        app,
        baseUrl: deployment.baseUrl(app),
        greeting,
        largeBodyBytes: target.largeBodyBytes,
        leg,
        notes,
        fetch: secretGuarded(deployment.fetch),
      };
    },
  };

  describe(name, () => {
    beforeAll(
      async () => {
        await target.setup().catch((error: unknown) => {
          setupFailure = { error };
        });
      },
      { timeout },
    );

    afterAll(
      async () => {
        if (!planned.keep) {
          await tearDown().catch(() => undefined);
          return;
        }
        const [last] = legs.slice(-1);
        if (last) {
          await cell.evidence
            .write(
              last,
              "kept.json",
              `${JSON.stringify({ slug, namespace: namespaceOfSlug(slug) }, null, 2)}\n`,
            )
            .catch(() => undefined);
        }
      },
      { timeout },
    );

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
