import { env, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it, vi } from "vitest";
import { newServePort, type PurgeQueue } from "../src/purge-queue";
import { openInBandQueue, openPurgeQueue } from "../src/purge-request";
import { newSqliteBacklogStore } from "../src/purge-sqlite-store";
import { buildOriginCall, type ServeProps } from "../src/serve-key";
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

describe("the SQLite backlog store", () => {
  it("forgets covered tags through an index rather than reading every tag it keeps", async () => {
    await runInDurableObject(freshQueue(), async (_instance, state) => {
      const store = newSqliteBacklogStore(state.storage);
      for (let i = 0; i < 20; i++) store.settleBatch("purge", [`r1|t${i}`], 10_000 + i);
      const forget = state.storage.sql
        .exec<{ detail: string }>(
          `EXPLAIN QUERY PLAN DELETE FROM purged
           WHERE MAX(COALESCE(purged_at, 0), COALESCE(invalidated_at, 0)) < ?`,
          5_000,
        )
        .toArray()
        .map((row) => row.detail);

      expect(forget.join("\n")).toMatch(/USING INDEX/);
    });
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

  it("purges the backlog's releases once for a backlog older than 60 seconds, and nothing after", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = freshQueue();
      const calls: string[] = [];
      await runInDurableObject(queue, async (instance) => {
        instance.serve = {
          purgeTags: async (mode, tags) => {
            calls.push(`${mode}:${tags.join(",")}`);
            return tags.includes("r1")
              ? { ok: true }
              : { ok: false, codes: [1134], message: "rate limited" };
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

      expect(calls).toEqual(["purge:r1|a", "purge:r1"]);
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

describe("the queue an in-band purge falls back to", () => {
  it("covers nothing the origin's later announcement of the same tag would purge", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const raisedAt = Date.now();
      const tag = "rinband|a";
      const exports = { PurgeQueue: env.PURGE_QUEUE };
      const fallback = openInBandQueue(exports) as DurableObjectStub<PurgeQueue>;
      const durable = openPurgeQueue(exports) as DurableObjectStub<PurgeQueue>;
      await runInDurableObject(fallback, async (instance) => {
        instance.serve = { purgeTags: async () => ({ ok: true }) };
      });

      await fallback.raise("purge", [{ tag, raisedAt: raisedAt + 500 }]);
      vi.setSystemTime(raisedAt + 2_000);
      await runDurableObjectAlarm(fallback);
      await durable.raise("purge", [{ tag, raisedAt }]);

      await runInDurableObject(durable, async (_instance, state) => {
        expect(newSqliteBacklogStore(state.storage).readBatch("purge", 100)).toContain(tag);
      });
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the in-band queue recording the purge times", () => {
  it("records the time of a tag it purges in the purge queue the next read asks", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const tag = "rrecorded|a";
      const exports = { PurgeQueue: env.PURGE_QUEUE };
      const fallback = openInBandQueue(exports) as DurableObjectStub<PurgeQueue>;
      const durable = openPurgeQueue(exports) as DurableObjectStub<PurgeQueue>;
      const purged: string[] = [];
      await runInDurableObject(fallback, async (instance) => {
        instance.serve = {
          purgeTags: async (_mode, tags) => {
            purged.push(...tags);
            return { ok: true };
          },
        };
      });

      await fallback.raise("purge", [{ tag, raisedAt: Date.now() }]);
      vi.setSystemTime(Date.now() + 2_000);
      await runDurableObjectAlarm(fallback);

      expect(purged).toContain(tag);
      expect((await durable.readTimes([tag]))[tag]?.purgedAt).toBeGreaterThanOrEqual(
        1_800_000_000_000,
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("records the expire of an invalidate it purges from its backlog", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const tag = "rexpiring|a";
      const expiresAt = 1_800_000_060_000;
      const exports = { PurgeQueue: env.PURGE_QUEUE };
      const fallback = openInBandQueue(exports) as DurableObjectStub<PurgeQueue>;
      const durable = openPurgeQueue(exports) as DurableObjectStub<PurgeQueue>;
      await runInDurableObject(fallback, async (instance) => {
        instance.serve = { purgeTags: async () => ({ ok: true }) };
      });

      await fallback.raise("invalidate", [{ tag, raisedAt: Date.now(), expiresAt }]);
      vi.setSystemTime(Date.now() + 2_000);
      await runDurableObjectAlarm(fallback);

      expect((await durable.readTimes([tag]))[tag]?.expiresAt).toBe(expiresAt);
    } finally {
      vi.useRealTimers();
    }
  });

  it("purges nothing, and keeps the tag pending, while the time cannot be recorded", async () => {
    const tag = "runrecorded|a";
    const exports = { PurgeQueue: env.PURGE_QUEUE };
    const fallback = openInBandQueue(exports) as DurableObjectStub<PurgeQueue>;
    const purged: string[] = [];
    await runInDurableObject(fallback, async (instance) => {
      instance.serve = {
        purgeTags: async (_mode, tags) => {
          purged.push(...tags);
          return { ok: true };
        },
      };
      const recorder = instance.recorder;
      instance.recorder = {
        record: async (mode, tags) => {
          if (tags.some((raised) => raised.tag === tag)) {
            throw new Error("the purge queue is unreachable");
          }
          await recorder.record(mode, tags);
        },
      };
    });

    await fallback.raise("purge", [{ tag, raisedAt: Date.now() }]);
    await runDurableObjectAlarm(fallback);

    expect(purged).not.toContain(tag);
    await runInDurableObject(fallback, async (_instance, state) => {
      expect(newSqliteBacklogStore(state.storage).readBatch("purge", 100)).toContain(tag);
    });
  });
});

describe("the PurgeQueue's port to Serve", () => {
  it("purges through a Serve stub with the props every page is stored under", async () => {
    const asked: ServeProps[] = [];
    const port = newServePort(({ props }) => {
      asked.push(props);
      return {
        purge: async () => ({ ok: true }) as const,
      };
    });

    await port.purgeTags("purge", ["r1|a"]);

    const stored = buildOriginCall("https://fn.example/blog", {
      host: "shop.example.com",
      app: "web",
      release: "r1",
      variant: ".rsc",
    }).props;
    expect(asked).toEqual([stored]);
  });
});
