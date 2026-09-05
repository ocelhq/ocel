import assert from "node:assert/strict";
import { afterAll, beforeAll, describe, it } from "bun:test";
import {
  type ContractRow,
  contractRows,
  INITIAL_GREETING,
  REDEPLOY_GREETING,
  secretGuarded,
} from "./contract";
import { evidence } from "./evidence";
import { currentRunIdentity, projectSlug } from "./identity";
import { ledgerFor } from "./ledger";
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
import { readPrepareFailure } from "./prepare";
import { cellNamed, environmentFrom, selectionFor } from "./selection";
import { type Cell, ladderTitle, type Leg, suitesOf } from "./spec";
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

export function describeCell(name: string) {
  const target = selectedTarget();
  describeSelected(cellNamed(selectionFor(target, environmentFrom()), name));
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function describeSelected({ name, example, variant }: Cell) {
  const target = selectedTarget();
  const runId = currentRunIdentity();
  const slug = projectSlug(name, runId);
  const dir = exampleDir(example.dir);
  const suites = suitesOf(example, variant);
  const cell: CellContext = {
    example,
    name,
    ...(variant === undefined ? {} : { variant }),
    suites,
    dir,
    slug,
    runId,
    evidence: evidence(evidenceDir(runId, target.name, name)),
  };

  const write = ledgerFor(runId, target.name, name);
  const timeout = target.legTimeoutMs;

  function testIn(key: string, title: string, work: () => Promise<void>) {
    it(
      title,
      async () => {
        const startTime = Date.now();
        try {
          await work();
          write({
            cell: key,
            title,
            outcome: "passed",
            startTime,
            duration: Date.now() - startTime,
          });
        } catch (error) {
          write({
            cell: key,
            title,
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
  }

  const rows = contractRows(suites);
  const hooks = example.hooks;
  const publishRows = hooks?.rows?.filter((row) => row.phase === "publish") ?? [];
  const consumeRows = hooks?.rows?.filter((row) => row.phase === "consume") ?? [];
  const outliveRows = hooks?.rows?.filter((row) => row.phase === "outlive") ?? [];
  const pruneRows = hooks?.rows?.filter((row) => row.phase === "prune") ?? [];

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
      testIn(cellKey(name, app), contractTitle(leg, row.title), async () => {
        await bringUp();
        await row.run(contractContext(app, leg));
      });
    }
  }

  function consumeLeg(leg: "contract" | "redeploy" | "rollback", app: string) {
    for (const row of consumeRows) {
      testIn(cellKey(name, app), ladderConsumeTitle(leg, row.title), async () => {
        await triggerBeforeUp();
        await row.run(cell, contractContext(app, leg));
      });
    }
  }

  function perAppDescribe(body: (app: string) => void) {
    for (const app of example.apps) {
      describe(cellKey(name, app), () => body(app));
    }
  }

  function perApp(title: string, work: () => Promise<void>) {
    perAppDescribe((app) => {
      testIn(cellKey(name, app), title, work);
    });
  }

  function contractPerApp(leg: "contract" | "redeploy" | "rollback") {
    perAppDescribe((app) => {
      contractLeg(leg, app, rows);
      consumeLeg(leg, app);
    });
  }

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
        await tearDown().catch(() => undefined);
      },
      { timeout },
    );

    perAppDescribe((app) => {
      if (hooks?.refuse) {
        const refuse = hooks.refuse;
        testIn(cellKey(name, app), REFUSE_TITLE, async () => {
          await refuse(cell);
        });
      }

      for (const row of publishRows) {
        testIn(cellKey(name, app), ladderTitle("publish", row.title), async () => {
          await triggerBeforeUp().catch(() => undefined);
          await row.run(cell);
        });
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

    perAppDescribe((app) => {
      for (const row of outliveRows) {
        testIn(cellKey(name, app), ladderTitle("outlive", row.title), async () => {
          await triggerBeforeUp();
          await row.run(cell);
        });
      }

      for (const row of pruneRows) {
        testIn(cellKey(name, app), ladderTitle("prune", row.title), async () => {
          await triggerBeforeUp();
          await triggerAfterDestroy();
          await row.run(cell);
        });
      }
    });
  });
}
