import { afterAll, beforeAll, describe, expect, inject } from "vitest";
import { checks } from "../src/checks/registry";
import { examples } from "../src/examples";
import { createTarget } from "../src/targets";
import type { TargetHandle } from "../src/types";

const targetName = process.env.OCEL_CONFORMANCE_TARGET ?? "dev";

for (const example of examples) {
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
      baseUrl: () => {
        if (!handle) throw new Error(`${example.name} target is not up`);
        return handle.baseUrl;
      },
      headObject: (key: string) => {
        if (!handle) throw new Error(`${example.name} target is not up`);
        return handle.headObject(key);
      },
    };
    for (const capability of example.capabilities) {
      checks[capability](context);
    }
  });
}
