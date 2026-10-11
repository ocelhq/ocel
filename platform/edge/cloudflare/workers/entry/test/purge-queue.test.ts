import { env, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it, vi } from "vitest";
import type { PurgeQueue } from "../src/purge-queue";
import { newSqliteBacklogStore } from "../src/purge-sqlite-store";
import { memoryBacklogStore } from "./purge-backlog-store";
import { describeBacklogStore } from "./purge-backlog-store-contract";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    PURGE_QUEUE: DurableObjectNamespace<PurgeQueue>;
  }
}

let objects = 0;
const freshQueue = () => env.PURGE_QUEUE.getByName(`queue-${objects++}`);

describeBacklogStore("memory", async (body) => {
  await body(memoryBacklogStore());
});

describeBacklogStore("SQLite", async (body) => {
  await runInDurableObject(freshQueue(), async (_instance, state) => {
    await body(newSqliteBacklogStore(state.storage));
  });
});

describe("the PurgeQueue object", () => {
  it("arms an alarm when a tag is raised", async () => {
    const queue = freshQueue();

    await queue.raise("purge", [{ tag: "r1|a", raisedAt: Date.now() }]);

    await runInDurableObject(queue, async (_instance, state) => {
      expect(await state.storage.getAlarm()).not.toBeNull();
    });
  });

  it("arms nothing for a tag that is already covered", async () => {
    const queue = freshQueue();
    await runInDurableObject(queue, async (_instance, state) => {
      const store = newSqliteBacklogStore(state.storage);
      store.settleBatch("purge", ["r1|a"], 2_000);
    });

    await queue.raise("purge", [{ tag: "r1|a", raisedAt: 1_000 }]);

    await runInDurableObject(queue, async (_instance, state) => {
      expect(await state.storage.getAlarm()).toBeNull();
    });
  });

  it("recovers once Cloudflare stops answering 1134: the tags stay pending, then purge", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = freshQueue();
      const calls: string[] = [];
      let limited = true;
      await runInDurableObject(queue, async (instance) => {
        instance.serve = {
          purgeTags: async (mode, tags) => {
            calls.push(`${mode}:${tags.join(",")}`);
            return limited ? { ok: false, codes: [1134], message: "rate limited" } : { ok: true };
          },
          purgeEverything: async () => ({ ok: true }),
        };
      });
      await queue.raise("purge", [{ tag: "r1|a", raisedAt: Date.now() }]);

      vi.setSystemTime(Date.now() + 1_000);
      await runDurableObjectAlarm(queue);
      limited = false;
      const refusedAt = Date.now();
      const armed = await runInDurableObject(queue, (_instance, state) => state.storage.getAlarm());
      vi.setSystemTime(armed! + 1);
      await runDurableObjectAlarm(queue);

      expect(calls).toEqual(["purge:r1|a", "purge:r1|a"]);
      expect(armed).toBeGreaterThan(refusedAt);
      await runInDurableObject(queue, async (_instance, state) => {
        expect(await state.storage.getAlarm()).toBeNull();
        expect(newSqliteBacklogStore(state.storage).hasAnyPending()).toBe(false);
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("purges everything once for a backlog older than 60 seconds, and nothing after", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = freshQueue();
      const calls: string[] = [];
      await runInDurableObject(queue, async (instance) => {
        instance.serve = {
          purgeTags: async (mode, tags) => {
            calls.push(`${mode}:${tags.join(",")}`);
            return { ok: false, codes: [1134], message: "rate limited" };
          },
          purgeEverything: async () => {
            calls.push("everything");
            return { ok: true };
          },
        };
      });
      await queue.raise("purge", [{ tag: "r1|a", raisedAt: Date.now() }]);
      vi.setSystemTime(Date.now() + 1_000);
      await runDurableObjectAlarm(queue);

      vi.setSystemTime(Date.now() + 61_000);
      await runDurableObjectAlarm(queue);
      vi.setSystemTime(Date.now() + 10_000);
      await runDurableObjectAlarm(queue);

      expect(calls).toEqual(["purge:r1|a", "everything"]);
      await runInDurableObject(queue, async (_instance, state) => {
        expect(await state.storage.getAlarm()).toBeNull();
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the tags pending and arms the alarm again when Serve cannot purge", async () => {
    const queue = freshQueue();
    await queue.raise("purge", [{ tag: "r1|a", raisedAt: Date.now() }]);

    expect(await runDurableObjectAlarm(queue)).toBe(true);

    await runInDurableObject(queue, async (_instance, state) => {
      expect(await state.storage.getAlarm()).not.toBeNull();
      expect(newSqliteBacklogStore(state.storage).readBatch("purge", 10)).toEqual(["r1|a"]);
      expect(newSqliteBacklogStore(state.storage).readFailures()).toBe(1);
    });
  });
  it("arms the alarm again when its own storage fails mid-flush, and logs the failure", async () => {
    const queue = freshQueue();
    await queue.raise("purge", [{ tag: "r1|a", raisedAt: Date.now() }]);
    await runInDurableObject(queue, async (instance, state) => {
      instance.serve = {
        purgeTags: async () => ({ ok: true }),
        purgeEverything: async () => ({ ok: true }),
      };
      state.storage.sql.exec("DROP TABLE purged");
    });
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      expect(await runDurableObjectAlarm(queue)).toBe(true);
      const armed = await runInDurableObject(queue, (_instance, state) => state.storage.getAlarm());
      expect(armed).not.toBeNull();
      expect(logged).toHaveBeenCalled();
    } finally {
      logged.mockRestore();
    }
  });
});
