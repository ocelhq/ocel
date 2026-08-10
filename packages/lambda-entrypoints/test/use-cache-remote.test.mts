import { afterEach, expect, test, vi } from "vitest";
import type { TagRecordUpdate } from "@ocel/next-cache";
import type {
  TagSnapshotRead,
  UseCacheEntry,
  UseCacheStore,
} from "../src/next/use-cache-store.mjs";
import { publishedRecords, type TagRow } from "./tag-rows.mjs";

function fakeStore() {
  const objects = new Map<string, UseCacheEntry>();
  const rows = new Map<string, TagRow>();
  let objectFailure: Error | null = null;
  let snapshotFailure: Error | null = null;

  const store: UseCacheStore & {
    objects: typeof objects;
    rows: typeof rows;
    seed(tag: string, row: TagRecordUpdate): void;
    breakObjects(): void;
    breakSnapshot(): void;
  } = {
    objects,
    rows,
    seed(tag, row) {
      rows.set(tag, { tag, ...row });
    },
    breakObjects() {
      objectFailure = new Error("s3 is down");
    },
    breakSnapshot() {
      snapshotFailure = new Error("s3 is down");
    },

    async readEntry(key) {
      if (objectFailure) throw objectFailure;
      return objects.get(key) ?? null;
    },

    async writeEntry(key, entry) {
      if (objectFailure) throw objectFailure;
      objects.set(key, entry);
    },

    async readTagSnapshot(): Promise<TagSnapshotRead> {
      await Promise.resolve();
      if (snapshotFailure) throw snapshotFailure;
      return { status: "fresh", records: publishedRecords(rows), etag: null };
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

afterEach(() => {
  delete process.env.OCEL_USE_CACHE_MAX_ENTRY;
});

async function load(store: UseCacheStore | null) {
  vi.resetModules();
  const clock = await import("../src/next/tag-clock.mjs");
  const handler = (await import("../src/next/use-cache-remote.mjs")).default;
  clock.setTagClockStore(store);
  return { tagClock: clock.tagClock, handler };
}

async function loadSynced(store: UseCacheStore) {
  const loaded = await load(store);
  await loaded.handler.refreshTags();
  expect(loaded.tagClock.hasSynced).toBe(true);
  return loaded;
}

function streamOf(body: string): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(Buffer.from(body)));
      controller.close();
    },
  });
}

function entry(body: string, over: Record<string, unknown> = {}) {
  return {
    value: streamOf(body),
    tags: [],
    stale: 0,
    timestamp: Date.now(),
    expire: 3600,
    revalidate: 60,
    ...over,
  };
}

async function readAll(stream: ReadableStream<Uint8Array>): Promise<string> {
  const reader = stream.getReader();
  let out = "";
  for (let chunk; !(chunk = await reader.read()).done; ) {
    out += Buffer.from(chunk.value).toString();
  }
  return out;
}

test("serves an entry back through the durable store", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set("k", Promise.resolve(entry("payload")));
  const hit = await handler.get("k", []);

  expect(hit).toBeDefined();
  expect(await readAll(hit!.value)).toBe("payload");
});

test("serves an entry written by an instance that no longer exists", async () => {
  const store = fakeStore();
  const writer = await loadSynced(store);
  await writer.handler.set("k", Promise.resolve(entry("payload")));

  const reader = await loadSynced(store);
  const hit = await reader.handler.get("k", []);

  expect(hit).toBeDefined();
  expect(await readAll(hit!.value)).toBe("payload");
});

test("makes a read arriving mid-fill wait for the fill rather than miss", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  let complete = (value: ReturnType<typeof entry>) => {
    void value;
  };
  const pendingEntry = new Promise<ReturnType<typeof entry>>((resolve) => {
    complete = resolve;
  });

  const writing = handler.set("/slow", pendingEntry);
  const reading = handler.get("/slow", []);

  complete(entry("filled"));
  await writing;

  const found = await reading;
  expect(found).toBeDefined();
  expect(await readAll(found!.value)).toBe("filled");
});

test("misses a key that was never stored", async () => {
  const { handler } = await loadSynced(fakeStore());

  expect(await handler.get("absent", [])).toBeUndefined();
});

test("rebuilds the value stream on every read", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set("k", Promise.resolve(entry("payload")));

  expect(await readAll((await handler.get("k", []))!.value)).toBe("payload");
  expect(await readAll((await handler.get("k", []))!.value)).toBe("payload");
});

test("serves nothing while the clock has never synced", async () => {
  const store = fakeStore();
  const { handler, tagClock } = await loadSynced(store);
  await handler.set("k", Promise.resolve(entry("payload")));

  const cold = await load(store);
  expect(cold.tagClock.hasSynced).toBe(false);
  expect(await cold.handler.get("k", [])).toBeUndefined();

  await cold.handler.refreshTags();
  expect(await cold.handler.get("k", [])).toBeDefined();
  expect(tagClock.hasSynced).toBe(true);
});

test("serves an entry past its revalidate duration rather than missing", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { timestamp: Date.now() - 10_000, revalidate: 5 })),
  );

  const hit = await handler.get("k", []);
  expect(hit).toBeDefined();
  expect(hit!.revalidate).toBe(5);
  expect(await readAll(hit!.value)).toBe("payload");
});

test("misses an entry past its expire duration whatever its revalidate says", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set(
    "k",
    Promise.resolve(
      entry("payload", { timestamp: Date.now() - 10_000, revalidate: 5, expire: 5 }),
    ),
  );

  expect(await handler.get("k", [])).toBeUndefined();
});

test("misses an entry whose explicit tag was revalidated after it was written", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  await handler.updateTags(["products"]);

  expect(await handler.get("k", [])).toBeUndefined();
});

test("leaves an entry whose tags were untouched alone", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  await handler.updateTags(["reviews"]);

  expect(await handler.get("k", [])).toBeDefined();
});

test("serves a tag-stale entry with the revalidate signal", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  await handler.updateTags(["products"], {});

  const hit = await handler.get("k", []);
  expect(hit).toBeDefined();
  expect(hit!.revalidate).toBe(-1);
  expect(await readAll(hit!.value)).toBe("payload");
});

test("misses an entry invalidated by another instance once the sync lands", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  expect(await handler.get("k", [])).toBeDefined();

  store.seed("products", { expired: Date.now(), writtenAt: Date.now() });
  const fresh = await loadSynced(store);

  expect(await fresh.handler.get("k", [])).toBeUndefined();
});

test("turns an object store outage into a miss rather than a throw", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);
  await handler.set("k", Promise.resolve(entry("payload")));

  store.breakObjects();

  await expect(handler.get("k", [])).resolves.toBeUndefined();
});

test("misses rather than throwing when the clock's own backend is out", async () => {
  const store = fakeStore();
  store.breakSnapshot();
  const { handler, tagClock } = await load(store);

  await handler.refreshTags();
  expect(tagClock.hasSynced).toBe(false);
  await expect(handler.get("k", [])).resolves.toBeUndefined();
});

test("swallows a failed write, costing only the entry", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);
  store.breakObjects();

  await expect(
    handler.set("k", Promise.resolve(entry("payload"))),
  ).resolves.toBeUndefined();
  expect(store.objects.size).toBe(0);
});

test("refuses an entry above the per-entry cap", async () => {
  process.env.OCEL_USE_CACHE_MAX_ENTRY = "50";
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await handler.set("small", Promise.resolve(entry("tiny")));
  await handler.set("huge", Promise.resolve(entry("x".repeat(500))));

  expect(await handler.get("huge", [])).toBeUndefined();
  expect(await handler.get("small", [])).toBeDefined();
});

test("leaves no entry behind when the value stream errors part-way", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  const torn = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new Uint8Array(Buffer.from("half")));
      controller.error(new Error("render blew up"));
    },
  });

  await expect(
    handler.set("k", Promise.resolve(entry("", { value: torn }))),
  ).resolves.toBeUndefined();
  expect(store.objects.size).toBe(0);
  expect(await handler.get("k", [])).toBeUndefined();
});

test("survives a pending entry that never materialises", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);

  await expect(
    handler.set("k", Promise.reject(new Error("render blew up"))),
  ).resolves.toBeUndefined();
  expect(await handler.get("k", [])).toBeUndefined();
});

test("misses without a durable backend at all", async () => {
  const { handler } = await load(null);

  await expect(
    handler.set("k", Promise.resolve(entry("payload"))),
  ).resolves.toBeUndefined();
  await expect(handler.get("k", [])).resolves.toBeUndefined();
});

test("round-trips every metadata field through the stored envelope", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);
  const timestamp = Date.now() - 1_000;

  await handler.set(
    "k",
    Promise.resolve(
      entry("payload", {
        tags: ["products"],
        stale: 30,
        timestamp,
        expire: 3600,
        revalidate: 60,
      }),
    ),
  );

  const hit = await handler.get("k", []);
  expect(hit).toMatchObject({
    tags: ["products"],
    stale: 30,
    timestamp,
    expire: 3600,
    revalidate: 60,
  });
});

test("leaves the caller's copy of the value stream intact", async () => {
  const store = fakeStore();
  const { handler } = await loadSynced(store);
  const pending = entry("payload");

  await handler.set("k", Promise.resolve(pending));

  expect(await readAll(pending.value)).toBe("payload");
});
