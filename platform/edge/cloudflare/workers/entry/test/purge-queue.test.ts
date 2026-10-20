import { env, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it, vi } from "vitest";
import { newServePort, type PurgeQueue, RETIREMENT_CHECK_EVERY_MS } from "../src/purge-queue";
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

const SCOPE = { slug: "p1", pagesPrefix: "pages/p1/prod/web/r00000001" };

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
      for (let i = 0; i < 20; i++) store.settleBatch("purge", [`r00000001|t${i}`], 10_000 + i);
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
  it("records a raised tag's time and purges it at once, leaving nothing pending", async () => {
    const queue = freshQueue();
    const purged: string[] = [];
    await runInDurableObject(queue, async (instance) => {
      instance.serve = {
        purgeTags: async (mode, tags) => {
          purged.push(`${mode}:${tags.join(",")}`);
          return { ok: true };
        },
      };
    });

    const raisedAt = Date.now();
    await queue.raise(SCOPE, "invalidate", [{ tag: "r00000001|a", raisedAt }]);

    expect(purged).toEqual(["invalidate:r00000001|a"]);
    expect(await queue.readTimes(["r00000001|a"])).toEqual({
      "r00000001|a": { invalidatedAt: raisedAt },
    });
    await runInDurableObject(queue, async (_instance, state) => {
      expect(newSqliteBacklogStore(state.storage).hasAnyPending()).toBe(false);
      expect(await state.storage.getAlarm()).toBeNull();
    });
  });

  it("refuses a raise for a prefix that names no release, and records nothing", async () => {
    const queue = freshQueue();

    await runInDurableObject(queue, async (instance) => {
      await expect(
        instance.raise({ slug: "p1", pagesPrefix: "not/a/release" }, "purge", [
          { tag: "r00000001|a", raisedAt: 7_000 },
        ]),
      ).rejects.toThrow(/not the pages prefix of a release/);
    });
    expect(await queue.readTimes(["r00000001|a"])).toEqual({});
  });

  it("arms an alarm when a raised tag cannot be purged at once", async () => {
    const queue = freshQueue();

    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: Date.now() }]);

    await runInDurableObject(queue, async (_instance, state) => {
      expect(await state.storage.getAlarm()).not.toBeNull();
    });
  });

  it("arms nothing for a tag that is already covered", async () => {
    const queue = freshQueue();
    await runInDurableObject(queue, async (_instance, state) => {
      const store = newSqliteBacklogStore(state.storage);
      store.settleBatch("purge", ["r00000001|a"], 2_000);
    });

    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: 1_000 }]);

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
      await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: Date.now() }]);

      limited = false;
      const refusedAt = Date.now();
      const armed = await runInDurableObject(queue, (_instance, state) => state.storage.getAlarm());
      vi.setSystemTime(armed! + 1);
      await runDurableObjectAlarm(queue);

      expect(calls).toEqual(["purge:r00000001|a", "purge:r00000001|a"]);
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
            return tags.includes("r00000001")
              ? { ok: true }
              : { ok: false, codes: [1134], message: "rate limited" };
          },
        };
      });
      await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: Date.now() }]);

      vi.setSystemTime(Date.now() + 61_000);
      await runDurableObjectAlarm(queue);
      vi.setSystemTime(Date.now() + 10_000);
      await runDurableObjectAlarm(queue);

      expect(calls).toEqual(["purge:r00000001|a", "purge:r00000001"]);
      await runInDurableObject(queue, async (_instance, state) => {
        expect(await state.storage.getAlarm()).toBeNull();
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the tags pending and arms the alarm again when Serve cannot purge", async () => {
    const queue = freshQueue();
    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: Date.now() }]);

    expect(await runDurableObjectAlarm(queue)).toBe(true);

    await runInDurableObject(queue, async (_instance, state) => {
      expect(await state.storage.getAlarm()).not.toBeNull();
      expect(newSqliteBacklogStore(state.storage).readBatch("purge", 10)).toEqual(["r00000001|a"]);
      expect(newSqliteBacklogStore(state.storage).readFailures()).toBe(1);
    });
  });

  it("arms the alarm again when its own storage fails mid-flush, and logs the failure", async () => {
    const queue = freshQueue();
    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: Date.now() }]);
    await runInDurableObject(queue, async (instance, state) => {
      instance.serve = {
        purgeTags: async () => ({ ok: true }),
      };
      newSqliteBacklogStore(state.storage).clearBackoff();
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

describe("the PurgeQueue's tag times", () => {
  it("keeps a tag's time past a day while its release is served", async () => {
    const queue = freshQueue();
    const now = Date.now();

    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|posts", raisedAt: now - 2 * 86_400_000 }]);
    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|other", raisedAt: now }]);

    expect((await queue.readTimes(["r00000001|posts"]))["r00000001|posts"]?.purgedAt).toBe(
      now - 2 * 86_400_000,
    );
  });

  it("evicts a release's cache on escalation without marking the release itself purged", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = freshQueue();
      await runInDurableObject(queue, async (instance) => {
        instance.serve = {
          purgeTags: async (_mode, tags) =>
            tags.includes("r00000001")
              ? { ok: true }
              : { ok: false, codes: [1134], message: "limited" },
        };
      });
      await queue.raise(SCOPE, "purge", [{ tag: "r00000001|a", raisedAt: Date.now() }]);
      vi.setSystemTime(Date.now() + 61_000);
      await runDurableObjectAlarm(queue);

      expect(await queue.readTimes(["r00000001", "r00000001|untouched"])).toEqual({});
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the PurgeQueue retiring a release", () => {
  type Served = "record" | "not-served" | "forgotten";
  const released = (answer: () => Promise<Served>) => ({
    readServedRecord: async () => {
      const kind = await answer();
      return kind === "record" ? { kind, record: {} as never } : { kind };
    },
  });

  async function raiseThenCheckAnHourOn(
    answer: () => Promise<Served>,
  ): Promise<DurableObjectStub<PurgeQueue>> {
    const queue = freshQueue();
    await runInDurableObject(queue, async (instance) => {
      instance.serve = { purgeTags: async () => ({ ok: true }) };
      instance.releases = released(answer);
    });
    await queue.raise(SCOPE, "invalidate", [{ tag: "r00000001|a", raisedAt: 1_800_000_000_000 }]);
    await queue.raise(SCOPE, "purge", [{ tag: "r00000001|b", raisedAt: 1_800_000_000_500 }]);
    vi.setSystemTime(1_800_000_000_000 + RETIREMENT_CHECK_EVERY_MS + 1);
    await queue.raise({ slug: "p1", pagesPrefix: "pages/p1/prod/web/r00000002" }, "purge", [
      { tag: "r00000002|a", raisedAt: Date.now() },
    ]);
    await runDurableObjectAlarm(queue);
    return queue;
  }

  it("replaces the tag times of a release no longer served with one purge time for the release, at its latest", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = await raiseThenCheckAnHourOn(async () => "not-served");

      expect(
        await queue.readTimes(["r00000001", "r00000001|a", "r00000001|b", "r00000002|a"]),
      ).toEqual({
        r00000001: { purgedAt: 1_800_000_000_500 },
        "r00000002|a": { purgedAt: 1_800_000_000_000 + RETIREMENT_CHECK_EVERY_MS + 1 },
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("forgets every tag time of a release no pointer serves or keeps, leaving no row for it", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = await raiseThenCheckAnHourOn(async () => "forgotten");

      expect(await queue.readTimes(["r00000001", "r00000001|a", "r00000001|b"])).toEqual({});
      await runInDurableObject(queue, async (_instance, state) => {
        const releases = state.storage.sql
          .exec<{ release: string }>(`SELECT release FROM recorded_releases ORDER BY release`)
          .toArray()
          .map((row) => row.release);
        expect(releases).toEqual(["r00000002"]);
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("forgets the purge time of a retired release once no pointer keeps it either", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      let served: Served = "not-served";
      const queue = await raiseThenCheckAnHourOn(async () => served);
      expect(Object.keys(await queue.readTimes(["r00000001"]))).toEqual(["r00000001"]);

      served = "forgotten";
      vi.setSystemTime(Date.now() + RETIREMENT_CHECK_EVERY_MS + 1);
      await queue.raise({ slug: "p1", pagesPrefix: "pages/p1/prod/web/r00000002" }, "purge", [
        { tag: "r00000002|b", raisedAt: Date.now() },
      ]);
      await runDurableObjectAlarm(queue);

      expect(await queue.readTimes(["r00000001", "r00000001|a", "r00000001|b"])).toEqual({});
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps every tag time of a release still served", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = await raiseThenCheckAnHourOn(async () => "record");

      expect(
        Object.keys(await queue.readTimes(["r00000001", "r00000001|a", "r00000001|b"])),
      ).toEqual(["r00000001|a", "r00000001|b"]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps every tag time of a release while the releases store cannot say whether it is served", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      vi.setSystemTime(1_800_000_000_000);
      const queue = await raiseThenCheckAnHourOn(async () => {
        throw new Error("releases store down");
      });

      expect(
        Object.keys(await queue.readTimes(["r00000001", "r00000001|a", "r00000001|b"])),
      ).toEqual(["r00000001|a", "r00000001|b"]);
      expect(logged).toHaveBeenCalled();
    } finally {
      logged.mockRestore();
      vi.useRealTimers();
    }
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

    await port.purgeTags("purge", ["r00000001|a"]);

    const stored = buildOriginCall("https://fn.example/blog", {
      host: "shop.example.com",
      app: "web",
      release: "r00000001",
      variant: ".rsc",
    }).props;
    expect(asked).toEqual([stored]);
  });
});
