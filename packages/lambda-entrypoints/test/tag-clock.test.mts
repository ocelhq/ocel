import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type {
  TagRecordPage,
  TagRecordRow,
  TagRecordUpdate,
  UseCacheStore,
} from "../src/next/use-cache-store.mjs";

// A stand-in for the state table's tag partition and its index: rows keyed by
// tag, queried in write order, and written under the same monotonic guard the
// real conditional update applies.
function fakeStore(pageSize = 100) {
  const rows = new Map<string, TagRecordRow>();
  let queries = 0;
  let failure: Error | null = null;

  const store: UseCacheStore & {
    rows: typeof rows;
    readonly queries: number;
    seed(tag: string, row: TagRecordUpdate): void;
    breakQueries(err?: Error): void;
    fixQueries(): void;
  } = {
    rows,
    get queries() {
      return queries;
    },
    seed(tag, row) {
      rows.set(tag, { tag, ...row });
    },
    breakQueries(err = new Error("dynamo is down")) {
      failure = err;
    },
    fixQueries() {
      failure = null;
    },

    async queryTagRecords(since, cursor): Promise<TagRecordPage> {
      queries++;
      // Yields, so concurrent callers really do overlap.
      await Promise.resolve();
      if (failure) throw failure;

      const ordered = [...rows.values()]
        .sort((a, b) => a.writtenAt - b.writtenAt)
        .filter((row) => row.writtenAt > since);
      const start = (cursor as number | undefined) ?? 0;
      const records = ordered.slice(start, start + pageSize);
      const next = start + records.length;
      return { records, cursor: next < ordered.length ? next : undefined };
    },

    async writeTag(tag, record) {
      const existing = rows.get(tag);
      if (existing?.expired !== undefined && existing.expired >= (record.expired ?? 0)) {
        return false;
      }
      rows.set(tag, { tag, ...record });
      return true;
    },
  };
  return store;
}

// The clock reads time through performance.now(), so shifting that is enough to
// step past the sync throttle without waiting on it.
let drift = 0;

beforeEach(() => {
  drift = 0;
  const real = performance.now.bind(performance);
  vi.spyOn(performance, "now").mockImplementation(() => real() + drift);
});

afterEach(() => {
  vi.restoreAllMocks();
  for (const v of ["OCEL_STATE_TABLE", "OCEL_ISR_TAG_NAMESPACE", "OCEL_STATE_TABLE_INDEX"]) {
    delete process.env[v];
  }
});

function advance(ms: number) {
  drift += ms;
}

// The clock's state is shared on globalThis and outlives a module reset, exactly
// as it does across the two handler bundles in a warm Lambda — so every test
// rebinds it onto its own store, which discards the previous one's state.
async function load(store: UseCacheStore | null, env: Record<string, string> = {}) {
  vi.resetModules();
  for (const [k, v] of Object.entries(env)) process.env[k] = v;
  const clock = await import("../src/next/tag-clock.mjs");
  const handler = (await import("../src/next/use-cache-default.mjs")).default;
  clock.setTagClockStore(store);
  return { tagClock: clock.tagClock, handler };
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

// Next fans updateTags out to every registered handler, so the second write for
// one event always loses the guard. Losing it is the common path, not an error.
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
  expect(store.queries).toBe(0);
});

test("a cold instance pages through the whole invalidation history", async () => {
  const store = fakeStore(2);
  const { handler } = await load(store);
  const at = Date.now();
  for (let i = 0; i < 5; i++) store.seed(`t${i}`, { expired: at, writtenAt: at + i });

  await handler.set("k", Promise.resolve(entry(["t4"])));
  await handler.refreshTags();

  expect(store.queries).toBe(3);
  expect(await handler.get("k", [])).toBeUndefined();
});

test("a truncated steady-state page resumes rather than skipping the remainder", async () => {
  const store = fakeStore(2);
  const { handler } = await load(store);

  // First sync is cold and finds nothing, which is what establishes the cursor.
  await handler.refreshTags();
  expect(store.queries).toBe(1);

  const at = Date.now();
  for (let i = 0; i < 4; i++) store.seed(`t${i}`, { expired: at, writtenAt: at + i });
  await handler.set("k", Promise.resolve(entry(["t3"])));

  advance(3_000);
  await handler.refreshTags();
  expect(store.queries).toBe(2);
  // t3 was on the second page, which a single steady-state page did not reach.
  expect(await handler.get("k", [])).toBeDefined();

  advance(3_000);
  await handler.refreshTags();
  expect(store.queries).toBe(3);
  expect(await handler.get("k", [])).toBeUndefined();
});

test("collapses concurrent syncs into a single query", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await Promise.all([handler.refreshTags(), handler.refreshTags()]);

  expect(store.queries).toBe(1);
});

test("suppresses a second sync inside the throttle window", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.refreshTags();
  await handler.refreshTags();

  expect(store.queries).toBe(1);
});

// Throttling on the attempt rather than the success is what keeps an already
// struggling table from being hit once per request.
test("a failing sync retries on a bounded interval rather than every request", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);
  store.breakQueries();

  await handler.refreshTags();
  await handler.refreshTags();
  await handler.refreshTags();
  expect(store.queries).toBe(1);
  expect(tagClock.hasSynced).toBe(false);

  advance(3_000);
  await handler.refreshTags();
  expect(store.queries).toBe(2);
});

test("a sync failure leaves the handler serving its last known tag state", async () => {
  const store = fakeStore();
  const { handler } = await load(store);

  await handler.set("k", Promise.resolve(entry(["products"])));
  await handler.refreshTags();

  store.breakQueries();
  advance(3_000);
  await expect(handler.refreshTags()).resolves.toBeUndefined();

  expect(await handler.get("k", [])).toBeDefined();
});

// The index is a substrate change that may not have been applied yet. Missing,
// the sync must degrade to the never-synced state rather than error.
test("a missing index degrades to the never-synced state", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);
  store.breakQueries(
    Object.assign(new Error("index not found"), { name: "ValidationException" }),
  );

  await expect(handler.refreshTags()).resolves.toBeUndefined();
  expect(tagClock.hasSynced).toBe(false);
});

test("reports having synced only once a sync has succeeded", async () => {
  const store = fakeStore();
  const { tagClock, handler } = await load(store);

  expect(tagClock.hasSynced).toBe(false);
  store.breakQueries();
  await handler.refreshTags();
  expect(tagClock.hasSynced).toBe(false);

  store.fixQueries();
  advance(3_000);
  await handler.refreshTags();
  expect(tagClock.hasSynced).toBe(true);
});

// Both handler bundles load their own copy of this module, so the state has to
// be shared — but only between copies that agree on what they are pointed at.
test("shares one clock between module graphs built from the same configuration", async () => {
  const config = {
    OCEL_STATE_TABLE: "state",
    OCEL_ISR_TAG_NAMESPACE: "TAG#a#",
    OCEL_STATE_TABLE_INDEX: "gsi1",
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
    OCEL_STATE_TABLE_INDEX: "gsi1",
  });
  await handler.updateTags(["products"]);

  vi.resetModules();
  process.env.OCEL_ISR_TAG_NAMESPACE = "TAG#b#";
  const other = (await import("../src/next/tag-clock.mjs")).tagClock;

  expect(await other.getExpiration(["products"])).toBe(0);
});

// Shipping ahead of the index means a clock with nothing behind it, which must
// still answer from its own writes rather than fail.
test("works with no durable store bound at all", async () => {
  const { handler } = await load(null);

  await handler.set("k", Promise.resolve(entry(["products"])));
  await expect(handler.refreshTags()).resolves.toBeUndefined();
  await expect(handler.updateTags(["products"])).resolves.toBeUndefined();

  expect(await handler.get("k", [])).toBeUndefined();
});
