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
  ladderConsumeTitle,
  REDEPLOY_TITLE,
  REFUSE_TITLE,
  ROLLBACK_TITLE,
  UP_TITLE,
} from "./plan";
import { type ExampleSpec, ladderTitle, type Leg } from "./spec";
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
  const hooks = example.hooks;
  const publishRows = hooks?.rows?.filter((row) => row.phase === "publish") ?? [];
  const consumeRows = hooks?.rows?.filter((row) => row.phase === "consume") ?? [];
  const outliveRows = hooks?.rows?.filter((row) => row.phase === "outlive") ?? [];
  const pruneRows = hooks?.rows?.filter((row) => row.phase === "prune") ?? [];

  let deployment: Deployment | undefined;
  let greeting = INITIAL_GREETING;
  const notes = new Map<string, string>();

  let setupFailure: { error: unknown } | undefined;

  const bringUp = once(async () => {
    if (setupFailure) {
      throw setupFailure.error;
    }
    deployment = await target.up(cell);
  });

  const tearDown = once(async () => {
    await target.destroy(cell);
  });

  const triggerBeforeUp = once(async () => {
    if (hooks?.beforeUp) {
      await hooks.beforeUp(cell);
    }
  });

  const triggerAfterDestroy = once(async () => {
    if (hooks?.afterDestroy) {
      await hooks.afterDestroy(cell);
    }
  });

  function contractContext(app: string, leg: Leg) {
    assert.ok(deployment, "the contract ran before the cell came up");
    return {
      app,
      baseUrl: deployment.baseUrl(app),
      greeting,
      leg,
      notes,
      fetch: secretGuarded(deployment.fetch),
    };
  }

  function contractLeg(leg: Leg, app: string, rowsForLeg: ContractRow[]) {
    for (const row of rowsForLeg) {
      it(
        contractTitle(leg, row.title),
        async () => {
          await bringUp();
          await row.run(contractContext(app, leg));
        },
        timeout,
      );
    }
  }

  function consumeLeg(leg: "contract" | "redeploy" | "rollback", app: string) {
    for (const row of consumeRows) {
      it(
        ladderConsumeTitle(leg, row.title),
        async () => {
          await triggerBeforeUp();
          await row.run(cell, contractContext(app, leg));
        },
        timeout,
      );
    }
  }

  function perAppDescribe(body: (app: string) => void) {
    for (const app of example.apps) {
      describe(cellKey(example.name, app), () => body(app));
    }
  }

  function perApp(title: string, work: () => Promise<void>) {
    perAppDescribe(() => {
      it(title, work, timeout);
    });
  }

  function contractPerApp(leg: "contract" | "redeploy" | "rollback") {
    perAppDescribe((app) => {
      contractLeg(leg, app, rows);
      consumeLeg(leg, app);
    });
  }

  describe(example.name, () => {
    beforeAll(async () => {
      await target.setup().catch((error: unknown) => {
        setupFailure = { error };
      });
    }, timeout);

    afterAll(async () => {
      await tearDown().catch(() => undefined);
    }, timeout);

    perAppDescribe(() => {
      if (hooks?.refuse) {
        const refuse = hooks.refuse;
        it(REFUSE_TITLE, () => refuse(cell), timeout);
      }

      for (const row of publishRows) {
        it(
          ladderTitle("publish", row.title),
          async () => {
            await triggerBeforeUp().catch(() => undefined);
            await row.run(cell);
          },
          timeout,
        );
      }
    });

    perApp(UP_TITLE, async () => {
      await triggerBeforeUp();
      await bringUp();
    });
    contractPerApp("contract");

    if (target.legs.includes("redeploy")) {
      const redeploy = target.redeploy;
      assert.ok(redeploy, `${target.name} declares a redeploy leg without a redeploy`);
      const redeployed = once(async () => {
        deployment = await redeploy(cell, REDEPLOY_GREETING);
        greeting = REDEPLOY_GREETING;
      });
      perApp(REDEPLOY_TITLE, async () => {
        await bringUp();
        await redeployed();
      });
      contractPerApp("redeploy");
    }

    if (target.legs.includes("rollback")) {
      const rollback = target.rollback;
      assert.ok(rollback, `${target.name} declares a rollback leg without a rollback`);
      const rolledBack = once(async () => {
        deployment = await rollback(cell, INITIAL_GREETING);
        greeting = INITIAL_GREETING;
      });
      perApp(ROLLBACK_TITLE, async () => {
        await bringUp();
        await rolledBack();
      });
      contractPerApp("rollback");
    }

    perApp(DESTROY_TITLE, async () => {
      await tearDown();
      assert.ok(
        !(await target.stands(slug)),
        `${slug} still exists on ${target.name} after destroy`,
      );
    });

    perAppDescribe(() => {
      for (const row of outliveRows) {
        it(
          ladderTitle("outlive", row.title),
          async () => {
            await triggerBeforeUp();
            await row.run(cell);
          },
          timeout,
        );
      }

      for (const row of pruneRows) {
        it(
          ladderTitle("prune", row.title),
          async () => {
            await triggerBeforeUp();
            await triggerAfterDestroy();
            await row.run(cell);
          },
          timeout,
        );
      }
    });
  });
}
