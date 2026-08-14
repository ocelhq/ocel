import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { TagRecordUpdate } from "@framework/next-cache";
import type { CacheEntryFile, CacheStore } from "../src/next/cache-store.mjs";
import type { TagSnapshotRead, UseCacheStore } from "../src/next/use-cache-store.mjs";
import { publishedRecords, type TagRow } from "./tag-rows.mjs";

function fakeStore() {
  const rows = new Map<string, TagRow>();
  const conditions: (string | null)[] = [];
  let version = 0;
  let published = true;
  let failure: Error | null = null;

  const store: UseCacheStore & {
    rows: typeof rows;
    readonly gets: number;
    readonly conditions: readonly (string | null)[];
    seed(tag: string, row: TagRecordUpdate): void;
    unpublish(): void;
    breakReads(err?: Error): void;
    fixReads(): void;
  } = {
    rows,
    get gets() {
      return conditions.length;
    },
    conditions,
    seed(tag, row) {
      rows.set(tag, { tag, ...row });
      version++;
    },
    unpublish() {
      published = false;
    },
    breakReads(err = new Error("s3 is down")) {
      failure = err;
    },
    fixReads() {
      failure = null;
    },

    async readTagSnapshot(etag): Promise<TagSnapshotRead> {
      conditions.push(etag);
      await Promise.resolve();
      if (failure) throw failure;
      if (!published) return { status: "unusable" };
      if (etag === `"v${version}"`) return { status: "unchanged" };

      return { status: "fresh", records: publishedRecords(rows), etag: `"v${version}"` };
    },

    async writeTag(tag, record) {
      const existing = rows.get(tag);
      if (existing?.expired !== undefined && existing.expired >= (record.expired ?? 0)) {
        return false;
      }
      rows.set(tag, { tag, ...record });
      version++;
      return true;
    },
  };
  return store;
}

let drift = 0;

beforeEach(() => {
  drift = 0;
  const real = performance.now.bind(performance);
  vi.spyOn(performance, "now").mockImplementation(() => real() + drift);
});

afterEach(async () => {
  (await import("../src/next/tags-manifest.mjs")).mirrorTagsInto(null);
  vi.restoreAllMocks();
  for (const v of [
    "OCEL_STATE_TABLE",
    "OCEL_ISR_TAG_NAMESPACE",
    "OCEL_ISR_BUCKET",
    "OCEL_ISR_PREFIX",
  ]) {
    delete process.env[v];
  }
});

function advance(ms: number) {
  drift += ms;
}

async function load(store: UseCacheStore | null, env: Record<string, string> = {}) {
  vi.resetModules();
  for (const [k, v] of Object.entries(env)) process.env[k] = v;
  const clock = await import("../src/next/tag-clock.mjs");
  const handler = (await import("../src/next/use-cache-default.mjs")).default;
  clock.setTagClockStore(store);
  return { tagClock: clock.tagClock, handler };
}

type FakeIsrStore = CacheStore & { entries: Map<string, CacheEntryFile> };

function fakeIsrStore(published: ReturnType<typeof fakeStore> | null): FakeIsrStore {
  const entries = new Map<string, CacheEntryFile>();
  return {
    entries,
    async readEntry(key) {
      return entries.get(key) ?? null;
    },
    async writeEntry(key, entry) {
      entries.set(key, entry);
    },
    async readFetch() {
      return null;
    },
    async writeFetch() {},
    async writeTags(tags, record) {
      const writtenAt = Date.now();
      for (const tag of tags) published?.seed(tag, { ...record, writtenAt });
    },
  };
}

function seedPage(isr: FakeIsrStore, tag: string, lastModified = 1_000) {
  isr.entries.set("index", {
    lastModified,
    value: {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      status: 200,
      headers: { "x-next-cache-tags": tag },
    },
  });
}

async function loadBoth(store: ReturnType<typeof fakeStore>, published = true) {
  vi.resetModules();
  const clock = await import("../src/next/tag-clock.mjs");
  const handler = (await import("../src/next/use-cache-default.mjs")).default;
  const CacheHandler = (await import("../src/next/cache-handler.mjs")).default;
  clock.setTagClockStore(store);
  const entries = fakeIsrStore(published ? store : null);
  CacheHandler.store = entries;
  return { tagClock: clock.tagClock, handler, isr: new CacheHandler(), entries };
}

async function invocation(fn: () => Promise<unknown>): Promise<Promise<unknown>[]> {
  const { runWithWaitUntil } = await import("../src/shared/background.mjs");
  const deferred: Promise<unknown>[] = [];
  await runWithWaitUntil((task) => {
    deferred.push(task);
  }, fn);
  return deferred;
}

function streamOf(body: string): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(Buffer.from(body)));
      controller.close();
    },
  });
}

function entry(tags: string[], over: Record<string, unknown> = {}) {
  return {
    value: streamOf("payload"),
    tags,
    stale: 0,
    timestamp: Date.now() - 1_000,
    expire: 3600,
    revalidate: 60,
    ...over,
  };
}

test("records an invalidation durably for other instances to find", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.updateTags(["products"]);

  const row = store.rows.get("products")!;
  expect(row.tag).toBe("products");
  expect(row.expired).toBeGreaterThan(0);
  expect(row.writtenAt).toBeGreaterThan(0);
});

test("an invalidation is visible to the raising instance with no sync in between", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.set("k", Promise.resolve(entry(["products"])));
  await handler.updateTags(["products"]);

  expect(await handler.get("k", [])).toBeUndefined();
});

test("an invalidation raised elsewhere is observed after the next sync", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.set("k", Promise.resolve(entry(["products"])));
  expect(await handler.get("k", [])).toBeDefined();

  store.seed("products", { expired: Date.now(), writtenAt: Date.now() });
  await handler.refreshTags();

  expect(await handler.get("k", [])).toBeUndefined();
});

test("swallows the rejection when a second writer loses the monotonic guard", async () => {
  const store = fakeStore();
  const { handler } = await load(store);
  const later = Date.now() + 60_000;
  store.seed("products", { expired: later, writtenAt: Date.now() });

  await expect(handler.updateTags(["products"])).resolves.toBeUndefined();

  expect(store.rows.get("products")!.expired).toBe(later);
});

test("never overwrites a later expiry with an earlier one", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.updateTags(["products"], { expire: 600 });
  const far = store.rows.get("products")!.expired!;
  await handler.updateTags(["products"], { expire: 1 });

  expect(store.rows.get("products")!.expired).toBe(far);
});

test("answers expiry lookups from memory without touching the network", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  expect(await handler.getExpiration(["never-seen"])).toBe(0);
  expect(store.gets).toBe(0);
});

test("a cold instance learns the whole invalidation history from one object", async () => {
  const store = fakeStore();
  const { handler } = await load(store);
  const at = Date.now();
  for (let i = 0; i < 5; i++) store.seed(`t${i}`, { expired: at, writtenAt: at + i });

  await handler.set("k", Promise.resolve(entry(["t4"])));
  await handler.refreshTags();

  expect(store.gets).toBe(1);
  expect(store.conditions).toEqual([null]);
  expect(await handler.get("k", [])).toBeUndefined();
});

test("conditions the next read on the version it holds and keeps its records on a 304", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);
  store.seed("products", { expired: Date.now(), writtenAt: Date.now() });

  await handler.refreshTags();
  const expiration = await tagClock.getExpiration(["products"]);
  expect(expiration).toBeGreaterThan(0);

  advance(3_000);
  await handler.refreshTags();

  expect(store.conditions).toEqual([null, '"v1"']);
  expect(tagClock.hasSynced).toBe(true);
  expect(await tagClock.getExpiration(["products"])).toBe(expiration);
});

test("a lagged snapshot cannot walk back this instance's own invalidation", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);

  await handler.updateTags(["products"], { expire: 600 });
  const mine = await tagClock.getExpiration(["products"]);
  store.seed("products", { expired: 1, writtenAt: 1 });

  advance(3_000);
  await handler.refreshTags();

  expect(await tagClock.getExpiration(["products"])).toBe(mine);
});

test("a snapshot with no records still counts as a sync", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);

  await handler.refreshTags();

  expect(tagClock.hasSynced).toBe(true);
});

test("collapses concurrent syncs into a single read", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await Promise.all([handler.refreshTags(), handler.refreshTags()]);

  expect(store.gets).toBe(1);
});

test("suppresses a second sync inside the throttle window", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.refreshTags();
  await handler.refreshTags();

  expect(store.gets).toBe(1);
});

test("a failing sync retries on a bounded interval rather than every request", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);
  store.breakReads();

  await handler.refreshTags();
  await handler.refreshTags();
  await handler.refreshTags();
  expect(store.gets).toBe(1);
  expect(tagClock.hasSynced).toBe(false);

  advance(3_000);
  await handler.refreshTags();
  expect(store.gets).toBe(2);
});

test("a sync failure leaves the handler serving its last known tag state", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.set("k", Promise.resolve(entry(["products"])));
  await handler.refreshTags();

  store.breakReads();
  advance(3_000);
  await expect(handler.refreshTags()).resolves.toBeUndefined();

  expect(await handler.get("k", [])).toBeDefined();
});

test("an unusable snapshot degrades to the never-synced state without throwing", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);
  store.unpublish();

  await expect(handler.refreshTags()).resolves.toBeUndefined();
  expect(tagClock.hasSynced).toBe(false);
});

test("reports having synced only once a sync has succeeded", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);

  expect(tagClock.hasSynced).toBe(false);
  store.breakReads();
  await handler.refreshTags();
  expect(tagClock.hasSynced).toBe(false);

  store.fixReads();
  advance(3_000);
  await handler.refreshTags();
  expect(tagClock.hasSynced).toBe(true);
});

test("shares one clock between module graphs built from the same configuration", async () => {
  const config = {
    OCEL_STATE_TABLE: "state",
    OCEL_ISR_TAG_NAMESPACE: "TAG#a#",
    OCEL_ISR_BUCKET: "assets",
    OCEL_ISR_PREFIX: "prod/proj/app/BID",
  };
  const store = fakeStore();
  const first = await load(store, config);
  await first.handler.updateTags(["products"]);

  vi.resetModules();
  const reloaded = (await import("../src/next/tag-clock.mjs")).tagClock;

  expect(reloaded).not.toBe(first.tagClock);
  expect(await reloaded.getExpiration(["products"])).toBeGreaterThan(0);
});

test("refuses to adopt a shared clock built from different configuration", async () => {
  const store = fakeStore();
  const { handler } = await load(store, {
    OCEL_STATE_TABLE: "state",
    OCEL_ISR_TAG_NAMESPACE: "TAG#a#",
    OCEL_ISR_BUCKET: "assets",
    OCEL_ISR_PREFIX: "prod/proj/app/BID",
  });
  await handler.updateTags(["products"]);

  vi.resetModules();
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#b#";
  const other = (await import("../src/next/tag-clock.mjs")).tagClock;

  expect(await other.getExpiration(["products"])).toBe(0);
});

test("an invalidation on the classic ISR model is the durable write and nothing more", async () => {
  const store = fakeStore();
  const { isr } = await loadBoth(store);

  const deferred = await invocation(() => isr.revalidateTag("products"));
  await Promise.all(deferred);

  expect(store.rows.get("products")!.expired).toBeGreaterThan(0);
  expect(store.gets).toBe(0);
});

test("an invalidation raised on either model is visible to the other at once", async () => {
  const store = fakeStore();
  const { tagClock, handler, isr } = await loadBoth(store);

  await handler.updateTags(["reviews"]);
  const deferred = await invocation(() => isr.revalidateTag("products"));
  await Promise.all(deferred);

  expect(await tagClock.getExpiration(["products"])).toBeGreaterThan(0);
  expect(await tagClock.getExpiration(["reviews"])).toBeGreaterThan(0);
});

test("works with no durable store bound at all", async () => {
  const { handler } = await load(null);

  await handler.set("k", Promise.resolve(entry(["products"])));
  await expect(handler.refreshTags()).resolves.toBeUndefined();
  await expect(handler.updateTags(["products"])).resolves.toBeUndefined();

  expect(await handler.get("k", [])).toBeUndefined();
});

test("the classic ISR read path pulls the snapshot itself", async () => {
  const store = fakeStore();
  const { isr, entries } = await loadBoth(store);
  seedPage(entries, "products");

  await isr.get("/", { kind: "APP_PAGE" });

  expect(store.gets).toBe(1);
});

test("an invalidation raised elsewhere expires an ISR entry once the read syncs", async () => {
  const store = fakeStore();
  const { isr, entries } = await loadBoth(store);
  seedPage(entries, "products");
  const at = Date.now();
  store.seed("products", { expired: at, writtenAt: at });

  expect(await isr.get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("serves a tagged ISR entry when the build has no usable snapshot", async () => {
  const store = fakeStore();
  store.unpublish();
  const { isr, entries } = await loadBoth(store);
  seedPage(entries, "products");

  expect(await isr.get("/", { kind: "APP_PAGE" })).not.toBeNull();
});

test("serves a tagged ISR entry through an object-store outage", async () => {
  const store = fakeStore();
  store.breakReads();
  const { isr, entries } = await loadBoth(store);
  seedPage(entries, "products");

  expect(await isr.get("/", { kind: "APP_PAGE" })).not.toBeNull();
});

test("an empty snapshot counts as a sync where an unusable one does not", async () => {
  const store = fakeStore();
  const empty = await loadBoth(store);
  seedPage(empty.entries, "products");

  expect(await empty.isr.get("/", { kind: "APP_PAGE" })).not.toBeNull();
  expect(empty.tagClock.hasSynced).toBe(true);

  store.unpublish();
  const unusable = await loadBoth(store);
  seedPage(unusable.entries, "products");

  expect(await unusable.isr.get("/", { kind: "APP_PAGE" })).not.toBeNull();
  expect(unusable.tagClock.hasSynced).toBe(false);
});

test("mirrors an invalidation it raises into Next's own tags manifest", async () => {
  const store = fakeStore();
  const manifest = new Map<string, { stale?: number; expired?: number }>();
  const { handler } = await load(store);
  (await import("../src/next/tags-manifest.mjs")).mirrorTagsInto(manifest);

  await handler.updateTags(["products"], { expire: 3600 });

  const mark = manifest.get("products")!;
  expect(mark.stale).toBeGreaterThan(0);
  expect(mark.expired).toBeGreaterThan(mark.stale!);
});

test("mirrors an invalidation raised elsewhere once it syncs", async () => {
  const store = fakeStore();
  const manifest = new Map<string, { stale?: number; expired?: number }>();
  const { tagClock } = await load(store);
  (await import("../src/next/tags-manifest.mjs")).mirrorTagsInto(manifest);

  store.seed("products", { stale: 4_000, expired: 9_000, writtenAt: 4_000 });
  await tagClock.refreshTags();

  expect(manifest.get("products")).toEqual({ stale: 4_000, expired: 9_000 });
});
