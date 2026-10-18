import { env, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { PurgeQueue } from "../src/purge-queue";
import { newSqliteTagTimes } from "../src/purge-times";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    PURGE_QUEUE: DurableObjectNamespace<PurgeQueue>;
  }
}

let objects = 0;
const freshQueue = () => env.PURGE_QUEUE.getByName(`times-${objects++}`);

const r1 = { release: "r1", slug: "p1", isrPrefix: "production/p1/web/r1/isr" };
const r10 = { release: "r10", slug: "p1", isrPrefix: "production/p1/web/r10/isr" };

describe("the times the PurgeQueue keeps for the tags raised", () => {
  it("reads back the latest purge and invalidation of a tag, apart", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);

      times.record(r1, "purge", [{ tag: "r1|a", raisedAt: 9_000 }]);
      times.record(r1, "invalidate", [{ tag: "r1|a", raisedAt: 9_500 }]);
      times.record(r1, "purge", [{ tag: "r1|a", raisedAt: 5_000 }]);

      expect(times.read(["r1|a", "r1|b"])).toEqual({
        "r1|a": { purgedAt: 9_000, invalidatedAt: 9_500 },
      });
    });
  });

  it("answers for no tag it was never told about", async () => {
    expect(await freshQueue().readTimes(["r1|never"])).toEqual({});
  });

  it("reads a hundred tags in one query", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      const tags = Array.from({ length: 100 }, (_, i) => `r1|t${i}`);
      times.record(
        r1,
        "purge",
        tags.map((tag) => ({ tag, raisedAt: 5 })),
      );

      expect(Object.keys(times.read(tags))).toHaveLength(100);
    });
  });

  it("names a release it recorded once its check is due, and not after it is checked", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      times.record(r1, "purge", [{ tag: "r1|a", raisedAt: 5 }]);

      expect(times.readReleasesCheckedBefore(Date.now() + 1, 10)).toEqual([r1]);
      times.markChecked("r1", Date.now() + 10);
      expect(times.readReleasesCheckedBefore(Date.now() + 1, 10)).toEqual([]);
    });
  });

  it("retires a release into one purge time at its latest tag time, and leaves other releases alone", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      times.record(r1, "purge", [{ tag: "r1|a", raisedAt: 5 }]);
      times.record(r1, "invalidate", [{ tag: "r1|b", raisedAt: 8 }]);
      times.record(r10, "purge", [{ tag: "r10|a", raisedAt: 3 }]);

      times.retire("r1");

      expect(times.read(["r1", "r1|a", "r1|b", "r10|a"])).toEqual({
        r1: { purgedAt: 8 },
        "r10|a": { purgedAt: 3 },
      });
      expect(times.readReleasesCheckedBefore(Date.now() + 1, 10)).toEqual([r10]);
    });
  });
});
