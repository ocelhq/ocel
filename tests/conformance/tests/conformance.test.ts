import { afterAll, beforeAll, describe, expect, inject } from "vitest";
import { checks } from "../src/checks/registry";
import { examples } from "../src/examples";
import { createTarget } from "../src/targets";
import type { TargetHandle } from "../src/types";
import type { TargetName } from "../src/types";

const targetName = process.env.OCEL_CONFORMANCE_TARGET ?? "dev";

for (const example of examples.filter(
  (example) =>
    !("targets" in example) ||
    (example.targets as readonly TargetName[]).includes(
      targetName as TargetName,
    ),
)) {
  describe(`${example.name} example (${targetName})`, () => {
    const target = createTarget(targetName, inject("accessToken"));
    const runId = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
    let handle: TargetHandle | undefined;

    beforeAll(async () => {
      handle = await target.up(example);
    }, 30 * 60_000);

    afterAll(async () => {
      await handle?.teardown();
      if (target.name === "dev" && example.framework === "next") {
        expect(handle?.output?.() ?? "").not.toMatch(
          /Uncached data[\s\S]*accessed outside[\s\S]*Suspense/,
        );
      }
    }, 30 * 60_000);

    const context = {
      example,
      runId,
      targetName: target.name,
      baseUrl: () => {
        if (!handle) throw new Error(`${example.name} target is not up`);
        return handle.baseUrl;
      },
      headObject: (key: string) => {
        if (!handle) throw new Error(`${example.name} target is not up`);
        return handle.headObject(key);
      },
      output: () => handle?.output?.() ?? "",
      linkReport: () => {
        if (!handle?.linkReport) {
          throw new Error(`${example.name} target has no published link report`);
        }
        return handle.linkReport;
      },
    };
    for (const capability of example.capabilities) {
      checks[capability](context);
    }
  });
}
