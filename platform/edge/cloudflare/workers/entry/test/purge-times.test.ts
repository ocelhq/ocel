import { env, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { PurgeQueue } from "../src/purge-queue";
import { newSqliteTagTimes, TAG_TIMES_RETENTION_MS } from "../src/purge-times";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    PURGE_QUEUE: DurableObjectNamespace<PurgeQueue>;
  }
}

let objects = 0;
const freshQueue = () => env.PURGE_QUEUE.getByName(`times-${objects++}`);

describe("the times the PurgeQueue keeps for the tags raised", () => {
  it("reads back the latest purge and invalidation of a tag, apart", async () => {
    const queue = freshQueue();
    const now = Date.now();

    await queue.record("purge", [{ tag: "r1|a", raisedAt: now - 1_000 }]);
    await queue.record("invalidate", [{ tag: "r1|a", raisedAt: now - 500 }]);
    await queue.record("purge", [{ tag: "r1|a", raisedAt: now - 5_000 }]);

    expect(await queue.readTimes(["r1|a", "r1|b"])).toEqual({
      "r1|a": { purgedAt: now - 1_000, invalidatedAt: now - 500 },
    });
  });

  it("reads back when an invalidation expires, the latest of the invalidations", async () => {
    const queue = freshQueue();
    const now = Date.now();

    await queue.record("invalidate", [
      { tag: "r1|a", raisedAt: now - 500, expiresAt: now + 60_000 },
    ]);
    await queue.record("invalidate", [
      { tag: "r1|a", raisedAt: now - 900, expiresAt: now + 5_000 },
    ]);

    expect(await queue.readTimes(["r1|a"])).toEqual({
      "r1|a": { invalidatedAt: now - 500, expiresAt: now + 60_000 },
    });
  });

  it("answers for no tag it was never told about", async () => {
    expect(await freshQueue().readTimes(["r1|never"])).toEqual({});
  });

  it("forgets a tag raised longer ago than it vouches for", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      times.record("purge", [{ tag: "r1|old", raisedAt: 1_000 }]);

      times.forgetBefore(1_000 + TAG_TIMES_RETENTION_MS + 1);

      expect(times.read(["r1|old"])).toEqual({});
    });
  });

  it("forgets an invalidation that never expires a day after it was raised", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      times.record("invalidate", [{ tag: "r1|default", raisedAt: 1_000 }]);

      times.forgetBefore(1_000 + TAG_TIMES_RETENTION_MS + 1);

      expect(times.read(["r1|default"])).toEqual({});
    });
  });

  it("keeps an invalidation until a day after it expires, and forgets it after", async () => {
    const week = 7 * 86_400_000;
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      times.record("invalidate", [{ tag: "r1|weekly", raisedAt: 1_000, expiresAt: 1_000 + week }]);

      times.forgetBefore(1_000 + TAG_TIMES_RETENTION_MS + 3_600_000);
      expect(times.read(["r1|weekly"])).toEqual({
        "r1|weekly": { invalidatedAt: 1_000, expiresAt: 1_000 + week },
      });

      times.forgetBefore(1_000 + week + TAG_TIMES_RETENTION_MS + 1);
      expect(times.read(["r1|weekly"])).toEqual({});
    });
  });

  it("reads a hundred tags in one query", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const times = newSqliteTagTimes(state.storage);
      const tags = Array.from({ length: 100 }, (_, i) => `r1|t${i}`);
      times.record(
        "purge",
        tags.map((tag) => ({ tag, raisedAt: 5 })),
      );

      expect(Object.keys(times.read(tags))).toHaveLength(100);
    });
  });
});
