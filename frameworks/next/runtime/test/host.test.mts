import type { CacheEntryFile } from "@framework/next-cache";
import { afterEach, expect, test, vi } from "vitest";
import type { CacheStore } from "../src/cache-store.mjs";
import type { UseCacheStore } from "../src/use-cache-store.mjs";

afterEach(async () => {
  (await import("../src/host.mjs")).installNextHost({});
  vi.resetModules();
});

function storeHolding(key: string, entry: CacheEntryFile): CacheStore {
  return {
    async readEntry(read) {
      return read === key ? entry : null;
    },
    async writeEntry() {},
    async readFetch() {
      return null;
    },
    async writeFetch() {},
    async writeTags() {},
  };
}

test("the cache handler reads entries from the store its host installed", async () => {
  const { installNextHost } = await import("../src/host.mjs");
  installNextHost({
    newCacheStore: async () =>
      storeHolding("index", {
        lastModified: 1_000,
        value: { kind: "PAGES", html: "<p>hi</p>", pageData: {}, status: 200, headers: {} },
      }),
  });
  const { default: OcelCacheHandler } = await import("../src/cache-handler.mjs");

  const hit = await new OcelCacheHandler().get("/index", { kind: "PAGES" });

  expect(hit?.value.html).toBe("<p>hi</p>");
});

test("a cache handler whose host installed no store misses rather than throwing", async () => {
  const { default: OcelCacheHandler } = await import("../src/cache-handler.mjs");

  expect(await new OcelCacheHandler().get("/index", { kind: "PAGES" })).toBeNull();
});

test("the tag clock syncs from the use-cache store its host installed", async () => {
  const snapshot: UseCacheStore = {
    async readEntry() {
      return null;
    },
    async writeEntry() {},
    async readTagSnapshot() {
      return { status: "fresh", records: { products: { expired: 1 } }, etag: null };
    },
    async writeTag() {
      return true;
    },
  };
  const { installNextHost } = await import("../src/host.mjs");
  installNextHost({ newUseCacheStore: async () => snapshot });
  const { tagClock, setTagClockStore } = await import("../src/tag-clock.mjs");
  setTagClockStore(undefined);

  await tagClock.refreshTags();

  expect(tagClock.hasSynced).toBe(true);
});

test("cache handlers that read at once before any store exists open one store between them", async () => {
  let opened = 0;
  const { installNextHost } = await import("../src/host.mjs");
  installNextHost({
    newCacheStore: async () => {
      opened++;
      await new Promise((resolve) => setTimeout(resolve, 10));
      return storeHolding("index", {
        lastModified: 1_000,
        value: { kind: "PAGES", html: "<p>hi</p>", pageData: {}, status: 200, headers: {} },
      });
    },
  });
  const { default: OcelCacheHandler } = await import("../src/cache-handler.mjs");

  await Promise.all([
    new OcelCacheHandler().get("/index", { kind: "PAGES" }),
    new OcelCacheHandler().get("/index", { kind: "PAGES" }),
  ]);

  expect(opened).toBe(1);
});

test("a use-cache store that failed to open is opened again on the next read", async () => {
  const store: UseCacheStore = {
    async readEntry() {
      return null;
    },
    async writeEntry() {},
    async readTagSnapshot() {
      return { status: "unchanged" };
    },
    async writeTag() {
      return true;
    },
  };
  let attempts = 0;
  const { installNextHost } = await import("../src/host.mjs");
  installNextHost({
    newUseCacheStore: async () => {
      attempts++;
      if (attempts === 1) throw new Error("the store is unreachable");
      return store;
    },
  });
  const { useCacheStore, setTagClockStore } = await import("../src/tag-clock.mjs");
  setTagClockStore(undefined);

  expect(await useCacheStore()).toBeNull();
  expect(await useCacheStore()).toBe(store);
});
