import { tagSnapshotKey, type TagSnapshot } from "@framework/next-cache";
import { describe, expect, it, vi } from "vitest";

import {
  createTagClock,
  dropSnapshotMemo,
  invalidateSnapshot,
  snapshotMaxAgeSeconds,
  snapshotTtlSeconds,
} from "../src/tag-clock";

const cfg = { isrPrefix: "prod/proj/app/build" };
const snapshotKey = tagSnapshotKey(cfg.isrPrefix);

const snapshot = (over: Partial<TagSnapshot> = {}): TagSnapshot => ({
  version: 1,
  deployedAt: 500,
  generatedAt: 900,
  records: {},
  ...over,
});

function storeWith(body: string | null, opts: { fail?: boolean } = {}) {
  const gets: string[] = [];
  return {
    gets,
    async get(key: string) {
      gets.push(key);
      if (opts.fail) throw new Error("store down");
      if (body === null) return null;
      return { etag: `"${key}"`, text: async () => body };
    },
  };
}

it("reports a tag expired after the entry was written as expired", async () => {
  const snap = snapshot({ records: { posts: { expired: 2_000 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("expired");
});

it("reports no expiry when the tag lapsed before the entry was written", async () => {
  const snap = snapshot({ records: { posts: { expired: 500 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("fresh");
});

it("reports a record whose expiry is still ahead as stale, not expired", async () => {
  const snap = snapshot({ records: { posts: { stale: 2_000, expired: 9_000 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("stale");
});

it("reports a stale mark predating the entry as fresh", async () => {
  const snap = snapshot({ records: { posts: { stale: 500, expired: 9_000 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("fresh");
});

it("reports no tags as fresh without reading the snapshot", async () => {
  const store = storeWith(null);
  const clock = createTagClock(cfg, { store });
  expect(await clock.freshness([], 1_000, 3_000)).toBe("fresh");
  expect(store.gets).toEqual([]);
});

it("returns 'untrusted' on a missing snapshot", async () => {
  const clock = createTagClock(cfg, { store: storeWith(null) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("answers from a snapshot however long ago it was published", async () => {
  const snap = snapshot({ generatedAt: 900, records: { posts: { expired: 2_000 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.freshness(["posts"], 1_000, 3_000e9)).toBe("expired");
});

it("returns 'untrusted' at a version this reader predates", async () => {
  const snap = { ...snapshot(), version: 2 };
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("returns 'untrusted' on a store error", async () => {
  const clock = createTagClock(cfg, { store: storeWith(null, { fail: true }) });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("memoizes an absent snapshot, so one request pays one read", async () => {
  const store = storeWith(null);
  const clock = createTagClock(cfg, { store });
  await clock.prime(3_000);
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
  expect(store.gets).toHaveLength(1);
});

it("re-reads an absent snapshot once the memo window has passed", async () => {
  const store = storeWith(null);
  const clock = createTagClock(cfg, { store });
  await clock.prime(3_000);
  await clock.freshness(["posts"], 1_000, 5_000);
  expect(store.gets).toHaveLength(2);
});

it("reads the snapshot object under the build prefix", async () => {
  const snap = snapshot();
  const store = storeWith(JSON.stringify(snap));
  const clock = createTagClock(cfg, { store });
  await clock.freshness(["posts"], 1_000, 3_000);
  expect(store.gets).toContain(snapshotKey);
});

it("re-reads inside the memo window once the memo is dropped", async () => {
  let body = JSON.stringify(snapshot());
  const gets: string[] = [];
  const store = {
    async get(key: string) {
      gets.push(key);
      return { etag: `"${key}"`, text: async () => body };
    },
  };
  const clock = createTagClock(cfg, { store });
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("fresh");

  body = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
  dropSnapshotMemo(cfg, store);
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("expired");
  expect(gets).toHaveLength(2);
});

describe("the PoP copy fronting the replica", () => {
  function popCache() {
    const entries = new Map<string, string>();
    return {
      entries,
      async match(request: Request) {
        const body = entries.get(request.url);
        return body === undefined ? undefined : new Response(body);
      },
      async put(request: Request, response: Response) {
        entries.set(request.url, await response.text());
      },
      async delete(request: Request) {
        return entries.delete(request.url);
      },
    };
  }

  it("answers a later read without going back to the store", async () => {
    const store = storeWith(JSON.stringify(snapshot()));
    const snapshotCache = popCache();
    const clock = createTagClock(cfg, { store, snapshotCache });

    await clock.freshness(["posts"], 1_000, 3_000);
    await clock.freshness(["posts"], 1_000, 9_000);

    expect(store.gets).toHaveLength(1);
  });

  it("re-reads the replica once the copy is purged", async () => {
    let body = JSON.stringify(snapshot());
    const gets: string[] = [];
    const store = {
      async get(key: string) {
        gets.push(key);
        return { etag: `"${key}"`, text: async () => body };
      },
    };
    const snapshotCache = popCache();
    const clock = createTagClock(cfg, { store, snapshotCache });

    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("fresh");

    body = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
    await invalidateSnapshot(cfg, { store, snapshotCache });

    expect(await clock.freshness(["posts"], 1_000, 9_000)).toBe("expired");
    expect(gets).toHaveLength(2);
  });

  it("purges the copy under the key the read path stored it at", async () => {
    const store = storeWith(JSON.stringify(snapshot()));
    const snapshotCache = popCache();
    const clock = createTagClock(cfg, { store, snapshotCache });

    await clock.freshness(["posts"], 1_000, 3_000);
    expect(snapshotCache.entries.size).toBe(1);

    await invalidateSnapshot(cfg, { store, snapshotCache });
    expect(snapshotCache.entries.size).toBe(0);
  });

  it("still drops the memo when the cache cannot purge", async () => {
    let body = JSON.stringify(snapshot());
    const gets: string[] = [];
    const store = {
      async get(key: string) {
        gets.push(key);
        return { etag: `"${key}"`, text: async () => body };
      },
    };
    const inert = {
      async match() {
        return undefined;
      },
      async put() {},
    };
    const clock = createTagClock(cfg, { store, snapshotCache: inert });

    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("fresh");
    body = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
    await invalidateSnapshot(cfg, { store, snapshotCache: inert });

    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("expired");
    expect(gets).toHaveLength(2);
  });
});

describe("readers arriving together on a cold memo", () => {
  function gatedStore(body: string | null, opts: { fail?: boolean } = {}) {
    const gets: string[] = [];
    let release!: () => void;
    const gate = new Promise<void>((done) => {
      release = done;
    });
    return {
      gets,
      release: () => release(),
      async get(key: string) {
        gets.push(key);
        await gate;
        if (opts.fail) throw new Error("store down");
        return body === null ? null : { etag: `"${key}"`, text: async () => body };
      },
    };
  }

  it("share one store read rather than one apiece", async () => {
    const store = gatedStore(JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } })));
    const clock = createTagClock(cfg, { store });

    const verdicts = Promise.all([
      clock.freshness(["posts"], 1_000, 3_000),
      clock.freshness(["posts"], 1_000, 3_000),
      clock.freshness(["posts"], 1_000, 3_000),
    ]);
    store.release();

    expect(await verdicts).toEqual(["expired", "expired", "expired"]);
    expect(store.gets).toHaveLength(1);
  });

  it("all get the failure a solo caller would have got, and it is not left behind", async () => {
    const failing = gatedStore(null, { fail: true });
    const clock = createTagClock(cfg, { store: failing });

    const verdicts = Promise.all([
      clock.freshness(["posts"], 1_000, 3_000),
      clock.freshness(["posts"], 1_000, 3_000),
    ]);
    failing.release();
    expect(await verdicts).toEqual(["untrusted", "untrusted"]);
    expect(failing.gets).toHaveLength(1);

    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
    expect(failing.gets).toHaveLength(2);
  });

  it("are not joined to a read that started before an invalidation", async () => {
    const bodies = [
      JSON.stringify(snapshot()),
      JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } })),
    ];
    let n = 0;
    let release!: () => void;
    const gate = new Promise<void>((done) => {
      release = done;
    });
    const gets: string[] = [];
    const store = {
      async get(key: string) {
        const body = bodies[Math.min(n++, bodies.length - 1)]!;
        gets.push(key);
        await gate;
        return { etag: `"${key}"`, text: async () => body };
      },
    };
    const clock = createTagClock(cfg, { store });

    const before = clock.freshness(["posts"], 1_000, 3_000);
    await invalidateSnapshot(cfg, { store });
    const after = clock.freshness(["posts"], 1_000, 3_000);
    release();

    expect(await before).toBe("fresh");
    expect(await after).toBe("expired");
    expect(gets).toHaveLength(2);
  });

  it("join the read that replaced an abandoned one, not a third of their own", async () => {
    const gets: string[] = [];
    const releases: Array<() => void> = [];
    let fail = true;
    const store = {
      async get(key: string) {
        gets.push(key);
        const doomed = fail;
        fail = false;
        await new Promise<void>((done) => releases.push(done));
        if (doomed) throw new Error("store down");
        return { etag: `"${key}"`, text: async () => JSON.stringify(snapshot()) };
      },
    };
    const clock = createTagClock(cfg, { store });

    const abandoned = clock.freshness(["posts"], 1_000, 3_000);
    await invalidateSnapshot(cfg, { store });
    const successor = clock.freshness(["posts"], 1_000, 3_000);

    releases[0]!();
    expect(await abandoned).toBe("untrusted");

    const joiner = clock.freshness(["posts"], 1_000, 3_000);
    for (let i = 0; i < 5; i++) {
      releases.forEach((done) => done());
      await Promise.resolve();
    }
    expect(await successor).toBe("fresh");
    expect(await joiner).toBe("fresh");
    expect(gets).toHaveLength(2);
  });

  it("do not have an abandoned read fill the memo or re-put the purged copy behind them", async () => {
    const stale = JSON.stringify(snapshot());
    const fresh = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
    const bodies = [stale, fresh];
    const gets: string[] = [];
    const releases: Array<() => void> = [];
    const store = {
      async get(key: string) {
        const body = bodies[Math.min(gets.length, bodies.length - 1)]!;
        gets.push(key);
        await new Promise<void>((done) => releases.push(done));
        return { etag: `"${key}"`, text: async () => body };
      },
    };
    const puts: string[] = [];
    let purged = 0;
    const snapshotCache = {
      async match() {
        return undefined;
      },
      async put(_request: Request, response: Response) {
        puts.push(await response.text());
      },
      async delete() {
        purged++;
        return true;
      },
    };
    const clock = createTagClock(cfg, { store, snapshotCache });

    const abandoned = clock.freshness(["posts"], 1_000, 3_000);
    await invalidateSnapshot(cfg, { store, snapshotCache });
    expect(purged).toBe(1);
    const successor = clock.freshness(["posts"], 1_000, 3_000);

    for (let i = 0; releases.length < 2 && i < 20; i++) {
      await new Promise((done) => setTimeout(done, 0));
    }
    expect(releases).toHaveLength(2);

    releases[1]!();
    expect(await successor).toBe("expired");
    releases[0]!();
    expect(await abandoned).toBe("fresh");

    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("expired");
    expect(gets).toHaveLength(2);
    expect(puts).toEqual([fresh]);
  });
});

describe("two builds sharing one binding", () => {
  const older = { isrPrefix: "prod/proj/app/build-n" };
  const newer = { isrPrefix: "prod/proj/app/build-n1" };

  function perPrefixStore() {
    const gets: string[] = [];
    let release!: () => void;
    const gate = new Promise<void>((done) => {
      release = done;
    });
    return {
      gets,
      release: () => release(),
      async get(key: string) {
        gets.push(key);
        await gate;
        const body =
          key === tagSnapshotKey(newer.isrPrefix)
            ? snapshot({ records: { posts: { expired: 2_000 } } })
            : snapshot();
        return { etag: `"${key}"`, text: async () => JSON.stringify(body) };
      },
    };
  }

  it("do not join each other's read", async () => {
    const store = perPrefixStore();
    const verdicts = Promise.all([
      createTagClock(older, { store }).freshness(["posts"], 1_000, 3_000),
      createTagClock(newer, { store }).freshness(["posts"], 1_000, 3_000),
    ]);
    store.release();

    expect(await verdicts).toEqual(["fresh", "expired"]);
    expect(store.gets).toEqual([tagSnapshotKey(older.isrPrefix), tagSnapshotKey(newer.isrPrefix)]);
  });

  it("do not answer from each other's memo", async () => {
    const store = perPrefixStore();
    store.release();

    expect(await createTagClock(older, { store }).freshness(["posts"], 1_000, 3_000)).toBe("fresh");
    expect(await createTagClock(newer, { store }).freshness(["posts"], 1_000, 3_000)).toBe("expired");
    expect(store.gets).toHaveLength(2);
  });
});

describe("a store read that never settles", () => {
  function neverSettlingStore() {
    let calls = 0;
    return {
      get calls() {
        return calls;
      },
      async get() {
        calls++;
        return new Promise<never>(() => {});
      },
    };
  }

  it("degrades to 'untrusted' once the bound elapses, rather than hanging forever", async () => {
    const store = neverSettlingStore();
    const clock = createTagClock(cfg, { store, snapshotReadTimeoutMs: 5 });
    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
  });

  it("does not hang prime() either", async () => {
    const store = neverSettlingStore();
    const clock = createTagClock(cfg, { store, snapshotReadTimeoutMs: 5 });
    expect(await clock.prime(3_000)).toBeNull();
  });

  it("clears the poisoned cell so the next request starts its own read", async () => {
    let calls = 0;
    const store = {
      async get(key: string) {
        calls++;
        if (calls === 1) return new Promise<never>(() => {});
        return { etag: `"${key}"`, text: async () => JSON.stringify(snapshot()) };
      },
    };
    const clock = createTagClock(cfg, { store, snapshotReadTimeoutMs: 5 });

    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("fresh");
    expect(calls).toBe(2);
  });
});

describe("the PoP copy's drawn lifetime", () => {
  function recordingCache() {
    const maxAges: number[] = [];
    return {
      maxAges,
      async match() {
        return undefined;
      },
      async put(_request: Request, response: Response) {
        const directive = response.headers.get("cache-control") ?? "";
        maxAges.push(Number(/max-age=(\d+)/.exec(directive)?.[1]));
      },
    };
  }

  it("is drawn over 7..10 seconds: never above the ceiling, never to nothing", () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0);
    try {
      expect(snapshotTtlSeconds).toBe(10);
      expect(snapshotMaxAgeSeconds()).toBe(10);
      random.mockReturnValue(0.999);
      expect(snapshotMaxAgeSeconds()).toBe(7);
    } finally {
      random.mockRestore();
    }
  });

  it("is drawn by the production path itself, not only by an injected one", async () => {
    const snapshotCache = recordingCache();
    for (let i = 0; i < 40; i++) {
      const clock = createTagClock(cfg, {
        store: storeWith(JSON.stringify(snapshot())),
        snapshotCache,
      });
      await clock.prime(3_000);
    }

    expect(snapshotCache.maxAges).toHaveLength(40);
    for (const maxAge of snapshotCache.maxAges) {
      expect([7, 8, 9, 10]).toContain(maxAge);
    }
    expect(new Set(snapshotCache.maxAges).size).toBeGreaterThanOrEqual(3);
  });
});

