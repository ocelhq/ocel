import { type TagSnapshot, tagSnapshotKey } from "@framework/next-cache";
import { describe, expect, it } from "vitest";

import { createTagClock, dropSnapshotMemo } from "../src/tag-clock";

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
  await clock.freshness(["posts"], 1_000, 3_000);
  expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
  expect(store.gets).toHaveLength(1);
});

it("re-reads an absent snapshot once the memo window has passed", async () => {
  const store = storeWith(null);
  const clock = createTagClock(cfg, { store });
  await clock.freshness(["posts"], 1_000, 3_000);
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
    dropSnapshotMemo(cfg, store);
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
    dropSnapshotMemo(cfg, store);
    const successor = clock.freshness(["posts"], 1_000, 3_000);

    releases[0]!();
    expect(await abandoned).toBe("untrusted");

    const joiner = clock.freshness(["posts"], 1_000, 3_000);
    for (let i = 0; i < 5; i++) {
      for (const done of releases) done();
      await Promise.resolve();
    }
    expect(await successor).toBe("fresh");
    expect(await joiner).toBe("fresh");
    expect(gets).toHaveLength(2);
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
    expect(await createTagClock(newer, { store }).freshness(["posts"], 1_000, 3_000)).toBe(
      "expired",
    );
    expect(store.gets).toHaveLength(2);
  });
});

describe("a store read that never finishes", () => {
  function neverFinishingStore() {
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
    const store = neverFinishingStore();
    const clock = createTagClock(cfg, { store, snapshotReadTimeoutMs: 5 });
    expect(await clock.freshness(["posts"], 1_000, 3_000)).toBe("untrusted");
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
