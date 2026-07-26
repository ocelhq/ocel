import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import {
  createTagClock,
  dropSnapshotMemo,
  invalidateSnapshot,
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
  // entry written at 1_000, tag expired at 2_000, now 3_000 => expired.
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(true);
});

it("reports no expiry when the tag lapsed before the entry was written", async () => {
  const snap = snapshot({ records: { posts: { expired: 500 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(false);
});

it("returns 'untrusted' on a missing snapshot", async () => {
  const clock = createTagClock(cfg, { store: storeWith(null) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("answers from a snapshot however long ago it was published", async () => {
  const snap = snapshot({ generatedAt: 900, records: { posts: { expired: 2_000 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.expired(["posts"], 1_000, 3_000e9)).toBe(true);
});

it("returns 'untrusted' at a version this reader predates", async () => {
  const snap = { ...snapshot(), version: 2 };
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("returns 'untrusted' on a store error", async () => {
  const clock = createTagClock(cfg, { store: storeWith(null, { fail: true }) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("memoizes an absent snapshot, so one request pays one read", async () => {
  const store = storeWith(null);
  const clock = createTagClock(cfg, { store });
  await clock.prime(3_000);
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
  expect(store.gets).toHaveLength(1);
});

it("re-reads an absent snapshot once the memo window has passed", async () => {
  const store = storeWith(null);
  const clock = createTagClock(cfg, { store });
  await clock.prime(3_000);
  await clock.expired(["posts"], 1_000, 5_000);
  expect(store.gets).toHaveLength(2);
});

it("reads the snapshot object under the build prefix", async () => {
  const snap = snapshot();
  const store = storeWith(JSON.stringify(snap));
  const clock = createTagClock(cfg, { store });
  await clock.expired(["posts"], 1_000, 3_000);
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
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(false);

  // A writer that republished the snapshot must see its own write, and the memo
  // window would otherwise answer from before it.
  body = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
  dropSnapshotMemo(store);
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(true);
  expect(gets).toHaveLength(2);
});

// The PoP copy is the layer an invalidation has to get past: it outlives the
// isolate, is shared by every isolate in the colo, and carries the longer TTL.
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

    await clock.expired(["posts"], 1_000, 3_000);
    // Past the isolate memo's window, so only the PoP copy can answer.
    await clock.expired(["posts"], 1_000, 9_000);

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

    expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(false);

    // What a Server Action's origin has already done by the time it answers.
    body = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
    await invalidateSnapshot(cfg, { store, snapshotCache });

    expect(await clock.expired(["posts"], 1_000, 9_000)).toBe(true);
    expect(gets).toHaveLength(2);
  });

  it("purges the copy under the key the read path stored it at", async () => {
    const store = storeWith(JSON.stringify(snapshot()));
    const snapshotCache = popCache();
    const clock = createTagClock(cfg, { store, snapshotCache });

    await clock.expired(["posts"], 1_000, 3_000);
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
    // A Cache API front with no delete, and one that throws, are the same case:
    // the memo still goes, and the copy lapses on its own TTL.
    const inert = {
      async match() {
        return undefined;
      },
      async put() {},
    };
    const clock = createTagClock(cfg, { store, snapshotCache: inert });

    expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(false);
    body = JSON.stringify(snapshot({ records: { posts: { expired: 2_000 } } }));
    await invalidateSnapshot(cfg, { store, snapshotCache: inert });

    // Same millisecond as the read above: only the memo drop can explain this.
    expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(true);
    expect(gets).toHaveLength(2);
  });
});
