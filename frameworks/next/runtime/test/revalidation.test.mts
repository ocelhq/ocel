import { afterEach, expect, test, vi } from "vitest";
import { purgedSince, purgeMark } from "../src/purge-signal.mjs";
import type { UseCacheStore } from "../src/use-cache-store.mjs";

function countingStore(delayMs = 0) {
  const written: string[] = [];
  let inFlight = 0;
  let most = 0;
  const store: UseCacheStore = {
    async readEntry() {
      return null;
    },
    async writeEntry() {},
    async readTagSnapshot() {
      return { status: "unusable" };
    },
    async writeTag(tag) {
      written.push(tag);
      inFlight++;
      most = Math.max(most, inFlight);
      await new Promise((resolve) => setTimeout(resolve, delayMs));
      inFlight--;
      return true;
    },
  };
  return { store, written, mostInFlight: () => most };
}

afterEach(async () => {
  (await import("../src/tags-manifest.mjs")).mirrorTagsInto(null);
  vi.restoreAllMocks();
});

async function loadHandlers(store: UseCacheStore) {
  vi.resetModules();
  const clock = await import("../src/tag-clock.mjs");
  clock.setTagClockStore(store);
  return {
    defaultCache: (await import("../src/use-cache-default.mjs")).default,
    remoteCache: (await import("../src/use-cache-remote.mjs")).default,
    IncrementalCache: (await import("../src/cache-handler.mjs")).default,
  };
}

test("one revalidation writes each tag once, though Next runs it through every cache handler", async () => {
  const { store, written } = countingStore();
  const { defaultCache, remoteCache, IncrementalCache } = await loadHandlers(store);
  const mark = purgeMark();
  const tags = ["products", "cart"];
  const durations = { expire: 600 };

  await Promise.all([
    defaultCache.updateTags(tags, durations),
    remoteCache.updateTags(tags, durations),
    new IncrementalCache().revalidateTag(tags, durations),
  ]);

  expect(written.sort()).toEqual(["cart", "products"]);
  expect(purgedSince(mark)).toHaveLength(2);
});

test("a later revalidation of the same tags writes them again", async () => {
  const { store, written } = countingStore();
  const { IncrementalCache } = await loadHandlers(store);

  await new IncrementalCache().revalidateTag(["products"]);
  await new IncrementalCache().revalidateTag(["products"]);

  expect(written).toEqual(["products", "products"]);
});

test("a request revalidating hundreds of tags keeps eight writes in flight", async () => {
  const { store, written, mostInFlight } = countingStore(1);
  const { IncrementalCache } = await loadHandlers(store);

  await new IncrementalCache().revalidateTag(Array.from({ length: 200 }, (_, i) => `t${i}`));

  expect(written).toHaveLength(200);
  expect(mostInFlight()).toBe(8);
});

test("an incremental-cache revalidation expires what the use-cache handlers read", async () => {
  const { store } = countingStore();
  const { defaultCache, IncrementalCache } = await loadHandlers(store);
  await defaultCache.set(
    "k",
    Promise.resolve({
      value: new Response("payload").body!,
      tags: ["products"],
      stale: 0,
      timestamp: Date.now() - 1_000,
      expire: 3600,
      revalidate: 60,
    }),
  );

  await new IncrementalCache().revalidateTag("products");

  expect(await defaultCache.get("k", [])).toBeUndefined();
});
