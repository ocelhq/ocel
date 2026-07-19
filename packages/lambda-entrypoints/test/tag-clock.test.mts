import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { TagSnapshot } from "@ocel/next-cache";
import type {
  StoredTagSnapshot,
  TagRecordPage,
  TagRecordRow,
  TagRecordUpdate,
  TagSnapshotStore,
  UseCacheStore,
} from "../src/next/use-cache-store.mjs";

// A stand-in for the object the edge reads: one versioned blob, written only
// under a matching etag, exactly as R2's conditional PUT behaves.
function fakeSnapshots() {
  let object: { body: string; etag: string } | null = null;
  let version = 0;
  let failure: Error | null = null;
  let interposed: TagSnapshot | null = null;
  let writes = 0;
  let reads = 0;

  const store: TagSnapshotStore & {
    readonly current: TagSnapshot | null;
    readonly writes: number;
    readonly reads: number;
    put(snapshot: TagSnapshot): void;
    putRaw(body: string): void;
    interposeOnce(snapshot: TagSnapshot): void;
    breakWrites(err?: Error): void;
    fixWrites(): void;
  } = {
    get current() {
      return object ? (JSON.parse(object.body) as TagSnapshot) : null;
    },
    get writes() {
      return writes;
    },
    get reads() {
      return reads;
    },
    put(snapshot) {
      object = { body: JSON.stringify(snapshot), etag: `v${++version}` };
    },
    putRaw(body) {
      object = { body, etag: `v${++version}` };
    },
    // Another publisher lands `snapshot` in the gap between our next read and
    // our next write, which is exactly what a precondition failure reports.
    interposeOnce(snapshot) {
      interposed = snapshot;
    },
    breakWrites(err = new Error("r2 is down")) {
      failure = err;
    },
    fixWrites() {
      failure = null;
    },

    async read(): Promise<StoredTagSnapshot | null> {
      reads++;
      await Promise.resolve();
      if (!object) return null;
      try {
        return { snapshot: JSON.parse(object.body) as TagSnapshot, etag: object.etag };
      } catch {
        return { snapshot: null, etag: object.etag };
      }
    },

    async write(snapshot, etag) {
      writes++;
      // Yields, so concurrent publishers really do interleave read and write.
      await Promise.resolve();
      if (failure) throw failure;
      if (interposed) {
        store.put(interposed);
        interposed = null;
      }
      // Mirrors If-Match / If-None-Match: a stale etag, or a create against an
      // object that now exists, is a precondition failure and not an error.
      if (etag === null ? object !== null : object?.etag !== etag) return false;
      object = { body: JSON.stringify(snapshot), etag: `v${++version}` };
      return true;
    },
  };
  return store;
}

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
    snapshots: ReturnType<typeof fakeSnapshots> | null;
    seed(tag: string, row: TagRecordUpdate): void;
    breakQueries(err?: Error): void;
    fixQueries(): void;
  } = {
    rows,
    snapshots: fakeSnapshots(),
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
        .filter((row) => row.writtenAt >= since);
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

// The snapshot is the edge's read-only replica of the clock. It is published
// from the sync drain rather than from updateTags, because only the drain holds
// every invalidation this instance has merged rather than just its own event.

async function publisher() {
  return (await import("../src/next/tag-snapshot.mjs")).publishTagSnapshot;
}

function snapshotOf(
  deployedAt: number,
  records: Record<string, { stale?: number; expired?: number }>,
): TagSnapshot {
  return { version: 1, deployedAt, generatedAt: deployedAt, validUntil: deployedAt, records };
}

test("publishes the merged clock as a snapshot the edge can read", async () => {
  const store = fakeStore();
  const { handler } = await load(store);
  const at = Date.now();
  store.seed("products", { expired: at, writtenAt: at });

  await handler.refreshTags();

  const snapshot = store.snapshots!.current!;
  expect(snapshot.version).toBe(1);
  expect(snapshot.records.products!.expired).toBe(at);
  expect(snapshot.generatedAt).toBeGreaterThan(0);
  // The window is what the edge trusts the replica within, so it has to be
  // declared ahead of the generation time rather than at it.
  expect(snapshot.validUntil).toBeGreaterThan(snapshot.generatedAt);
});

// The whole reason the write is safe under concurrency: the merge only moves
// watermarks upward, so whichever publisher wins a race produces a snapshot
// carrying both publishers' invalidations.
test("concurrent publishers converge with no invalidation lost", async () => {
  const store = fakeStore();
  const publish = await publisher();

  await Promise.all([
    publish(store, new Map([["a", { expired: 100 }]])),
    publish(store, new Map([["b", { expired: 200 }]])),
  ]);

  const records = store.snapshots!.current!.records;
  expect(records.a!.expired).toBe(100);
  expect(records.b!.expired).toBe(200);
});

test("a publisher carrying an older watermark never walks back a newer one", async () => {
  const store = fakeStore();
  const publish = await publisher();

  await publish(store, new Map([["products", { expired: 900, stale: 900 }]]));
  await publish(store, new Map([["products", { expired: 100, stale: 100 }]]));

  expect(store.snapshots!.current!.records.products).toEqual({
    expired: 900,
    stale: 900,
  });
});

test("re-reads and retries when another publisher wins the write", async () => {
  const store = fakeStore();
  const publish = await publisher();
  store.snapshots!.interposeOnce(snapshotOf(0, { theirs: { expired: 500 } }));

  await expect(publish(store, new Map([["ours", { expired: 700 }]]))).resolves.toBe(
    true,
  );

  const records = store.snapshots!.current!.records;
  expect(records.theirs!.expired).toBe(500);
  expect(records.ours!.expired).toBe(700);
});

// Every entry under a build's prefix was written at or after the build deployed,
// so a record whose watermarks both sit at or before that instant can no longer
// expire anything in it. Dropping those is what keeps the blob bounded on a
// substrate that has been invalidating tags for months.
test("prunes records that cannot apply to any entry in this build", async () => {
  const store = fakeStore();
  const publish = await publisher();
  store.snapshots!.put(
    snapshotOf(5_000, {
      before: { expired: 4_000 },
      atDeploy: { expired: 5_000 },
      after: { expired: 6_000 },
      staleOnly: { stale: 6_000 },
    }),
  );

  await publish(store, new Map());

  const records = store.snapshots!.current!.records;
  expect(Object.keys(records).sort()).toEqual(["after", "staleOnly"]);
});

// Without the deploy's own timestamp there is no proof a record is inert, and
// over-pruning would silently resurrect stale content at the edge.
test("prunes nothing from a snapshot that was never anchored to a deploy", async () => {
  const store = fakeStore();
  const publish = await publisher();
  store.snapshots!.put(snapshotOf(0, { ancient: { expired: 1 } }));

  await publish(store, new Map());

  expect(store.snapshots!.current!.records.ancient!.expired).toBe(1);
});

test("carries the build's deploy anchor forward across publishes", async () => {
  const store = fakeStore();
  const publish = await publisher();
  store.snapshots!.put(snapshotOf(5_000, {}));

  await publish(store, new Map([["products", { expired: 9_000 }]]));

  expect(store.snapshots!.current!.deployedAt).toBe(5_000);
});

test("a failed publish is repaired by the next one", async () => {
  const store = fakeStore();
  const { handler } = await load(store);
  const at = Date.now();
  store.seed("products", { expired: at, writtenAt: at });

  store.snapshots!.breakWrites();
  await expect(handler.refreshTags()).resolves.toBeUndefined();
  expect(store.snapshots!.current).toBeNull();

  store.snapshots!.fixWrites();
  advance(3_000);
  await handler.refreshTags();

  expect(store.snapshots!.current!.records.products!.expired).toBe(at);
});

// A blob that cannot be parsed still has an etag, so it is replaced under the
// same compare-and-swap rather than wedging the key for the life of the build.
test("overwrites a torn snapshot instead of wedging on it", async () => {
  const store = fakeStore();
  const publish = await publisher();
  store.snapshots!.putRaw("{not json");

  await expect(publish(store, new Map([["products", { expired: 700 }]]))).resolves.toBe(
    true,
  );

  expect(store.snapshots!.current!.records.products!.expired).toBe(700);
});

// The publisher runs on every sync, but the snapshot only has to be rewritten
// when something moved or when its trust window needs renewing — otherwise a
// busy instance would pay a round trip every sync interval to learn nothing.
test("does not rewrite the snapshot when no record has moved", async () => {
  const store = fakeStore();
  const { handler } = await load(store);
  const at = Date.now();
  store.seed("products", { expired: at, writtenAt: at });

  await handler.refreshTags();
  expect(store.snapshots!.writes).toBe(1);

  advance(3_000);
  await handler.refreshTags();
  expect(store.snapshots!.writes).toBe(1);
});

// A fully intercepted workload wakes no Lambda, so the window has to be renewed
// while the instance is still warm rather than after the edge falls open.
test("republishes to renew the trust window once it is half spent", async () => {
  const store = fakeStore();
  const { handler } = await load(store);
  const { snapshotRefreshMs } = await import("../src/next/tag-snapshot.mjs");

  await handler.refreshTags();
  expect(store.snapshots!.writes).toBe(1);

  advance(snapshotRefreshMs);
  await handler.refreshTags();
  expect(store.snapshots!.writes).toBe(2);
});

// An unadopted substrate has no edge replica to keep, so the clock has to behave
// exactly as it did before replication existed.
test("publishes nothing when the substrate adopted no object store", async () => {
  const store = fakeStore();
  store.snapshots = null;
  const { handler } = await load(store);
  const at = Date.now();
  store.seed("products", { expired: at, writtenAt: at });

  await expect(handler.refreshTags()).resolves.toBeUndefined();
  await expect(handler.updateTags(["reviews"])).resolves.toBeUndefined();

  await handler.set("k", Promise.resolve(entry(["products"])));
  expect(await handler.get("k", [])).toBeUndefined();
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
