import assert from "node:assert/strict";
import { afterAll, beforeAll, describe, it } from "vitest";
import {
  type ContractRow,
  contractRows,
  INITIAL_GREETING,
  REDEPLOY_GREETING,
  secretGuarded,
} from "./contract";
import { evidence } from "./evidence";
import { currentRunIdentity, projectSlug } from "./identity";
import { evidenceDir, exampleDir } from "./paths";
import {
  cellKey,
  contractTitle,
  DESTROY_TITLE,
  REDEPLOY_TITLE,
  ROLLBACK_TITLE,
  UP_TITLE,
} from "./plan";
import type { ExampleSpec, Leg } from "./spec";
import { selectedTarget } from "./targets";
import type { CellContext, Deployment } from "./targets/types";

type Once<T> = () => Promise<T>;

function once<T>(work: Once<T>): Once<T> {
  let started: Promise<T> | undefined;
  return () => {
    started ??= work();
    return started;
  };
}

export function describeCell(example: ExampleSpec) {
  const target = selectedTarget();
  const runId = currentRunIdentity();
  const slug = projectSlug(example.name, runId);
  const dir = exampleDir(example.dir);
  const cell: CellContext = {
    example,
    dir,
    slug,
    runId,
    evidence: evidence(evidenceDir(runId, target.name, example.name)),
  };

  const rows = contractRows(example.suites);
  const timeout = target.legTimeoutMs;

  let deployment: Deployment | undefined;
  let greeting = INITIAL_GREETING;

  const bringUp = once(async () => {
    deployment = await target.up(cell);
  });

  const tearDown = once(async () => {
    await target.destroy(cell);
  });

  function contractContext(app: string) {
    assert.ok(deployment, "the contract ran before the cell came up");
    return {
      app,
      baseUrl: deployment.baseUrl(app),
      greeting,
      fetch: secretGuarded(deployment.fetch),
    };
  }

  function contractLeg(leg: Leg, app: string, rowsForLeg: ContractRow[]) {
    for (const row of rowsForLeg) {
      it(
        contractTitle(leg, row.title),
        async () => {
          await bringUp();
          await row.run(contractContext(app));
        },
        timeout,
      );
    }
  }

  describe(example.name, () => {
    beforeAll(async () => {
      await target.setup();
    }, timeout);

    afterAll(async () => {
      await tearDown().catch(() => undefined);
    }, timeout);

    for (const app of example.apps) {
      describe(cellKey(example.name, app), () => {
        it(UP_TITLE, async () => {
          await bringUp();
        }, timeout);

        contractLeg("contract", app, rows);

        if (target.legs.includes("redeploy")) {
          const redeploy = target.redeploy;
          assert.ok(redeploy, `${target.name} declares a redeploy leg without a redeploy`);
          it(REDEPLOY_TITLE, async () => {
            await bringUp();
            deployment = await redeploy(cell, REDEPLOY_GREETING);
            greeting = REDEPLOY_GREETING;
          }, timeout);
          contractLeg("redeploy", app, rows);
        }

        if (target.legs.includes("rollback")) {
          const rollback = target.rollback;
          assert.ok(rollback, `${target.name} declares a rollback leg without a rollback`);
          it(ROLLBACK_TITLE, async () => {
            await bringUp();
            deployment = await rollback(cell, INITIAL_GREETING);
            greeting = INITIAL_GREETING;
          }, timeout);
          contractLeg("rollback", app, rows);
        }
      });
    }

    for (const app of example.apps) {
      describe(cellKey(example.name, app), () => {
        it(DESTROY_TITLE, async () => {
          await tearDown();
          const remaining = await target.list();
          assert.ok(
            !remaining.includes(slug),
            `${slug} still exists on ${target.name} after destroy: ${remaining.join(", ")}`,
          );
        }, timeout);
      });
    }
  });
}
