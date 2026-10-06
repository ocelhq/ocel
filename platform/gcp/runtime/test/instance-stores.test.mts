import type { CacheEntryFile } from "@framework/next-cache";
import { newInstanceCache } from "@framework/next-runtime/instance-cache";
import { expect, test } from "vitest";
import { newInstanceCacheStore, newInstanceUseCacheStore } from "../src/next/instance-stores.mjs";

function stringBytes(entry: unknown): number {
  return 2 * JSON.stringify(entry).length;
}

function page(html: string, lastModified = 1_000): CacheEntryFile {
  return { lastModified, value: { kind: "PAGES", html, pageData: {}, status: 200, headers: {} } };
}

test("an instance cache store reads back the entry it was handed", async () => {
  const store = newInstanceCacheStore(newInstanceCache(1024 * 1024));

  await store.writeEntry("/blog", page("<p>blog</p>"));

  expect(await store.readEntry("/blog")).toEqual(page("<p>blog</p>"));
  expect(await store.readEntry("/about")).toBeNull();
});

test("an instance cache store keeps fetch entries apart from page entries of the same key", async () => {
  const store = newInstanceCacheStore(newInstanceCache(1024 * 1024));

  await store.writeEntry("same", page("<p>page</p>"));
  await store.writeFetch("same", page("fetched"));

  expect(await store.readEntry("same")).toEqual(page("<p>page</p>"));
  expect(await store.readFetch("same")).toEqual(page("fetched"));
});

test("an instance cache store drops the least recently read entry once it outgrows its budget", async () => {
  const one = page("x".repeat(400));
  const budget = 2 * stringBytes(one) + 10;
  const store = newInstanceCacheStore(newInstanceCache(budget));

  await store.writeEntry("/a", one);
  await store.writeEntry("/b", one);
  await store.readEntry("/a");
  await store.writeEntry("/c", one);

  expect(await store.readEntry("/a")).not.toBeNull();
  expect(await store.readEntry("/b")).toBeNull();
  expect(await store.readEntry("/c")).not.toBeNull();
});

test("an instance cache store keeps no entry larger than its whole budget", async () => {
  const store = newInstanceCacheStore(newInstanceCache(100));

  await store.writeEntry("/huge", page("x".repeat(1_000)));

  expect(await store.readEntry("/huge")).toBeNull();
});

test("an instance cache store takes tag writes that reach only the writing instance's own tag clock", async () => {
  await expect(
    newInstanceCacheStore(newInstanceCache(1024)).writeTags(["products"], { expired: 1 }),
  ).resolves.toBeUndefined();
});

test("an instance use-cache store reads back the entry it was handed", async () => {
  const store = newInstanceUseCacheStore(newInstanceCache(1024 * 1024));
  const entry = { tags: ["a"], stale: 1, timestamp: 2, expire: 3, revalidate: 4, body: "Ym9keQ==" };

  await store.writeEntry("key", entry);

  expect(await store.readEntry("key")).toEqual(entry);
  expect(await store.readEntry("other")).toBeNull();
});

test("an instance use-cache store has a tag snapshot with no tags another instance wrote", async () => {
  const store = newInstanceUseCacheStore(newInstanceCache(1024));

  expect(await store.readTagSnapshot(null)).toEqual({ status: "fresh", records: {}, cursor: null });
  expect(await store.writeTag("cart", { expired: 1, writtenAt: 1 })).toBe(true);
});

test("an instance store charges an entry two bytes a character, the most V8 spends on a string", async () => {
  const entry = page("日".repeat(400));
  const fits = newInstanceCacheStore(newInstanceCache(stringBytes(entry)));
  const short = newInstanceCacheStore(newInstanceCache(stringBytes(entry) - 1));

  await fits.writeEntry("/wide", entry);
  await short.writeEntry("/wide", entry);

  expect(await fits.readEntry("/wide")).not.toBeNull();
  expect(await short.readEntry("/wide")).toBeNull();
});

test("an instance store hands back a copy, so mutating a read or written value leaves the cached entry as written", async () => {
  const store = newInstanceCacheStore(newInstanceCache(1024 * 1024));
  const written = page("<p>blog</p>");

  await store.writeEntry("/blog", written);
  (written.value as { html: string }).html = "<p>mutated after write</p>";
  const first = await store.readEntry("/blog");
  (first?.value as { html: string }).html = "<p>mutated after read</p>";

  expect(await store.readEntry("/blog")).toEqual(page("<p>blog</p>"));
});

test("an instance cache store and use-cache store handed one instance cache evict each other's entries once the cache outgrows its budget", async () => {
  const one = page("x".repeat(400));
  const useCacheEntry = {
    tags: [],
    stale: 1,
    timestamp: 2,
    expire: 3,
    revalidate: 4,
    body: "x".repeat(400),
  };
  const shared = newInstanceCache(stringBytes(one) + stringBytes(useCacheEntry) + 10);
  const pages = newInstanceCacheStore(shared);
  const useCache = newInstanceUseCacheStore(shared);

  await pages.writeEntry("/a", one);
  await useCache.writeEntry("key", useCacheEntry);
  await pages.writeEntry("/b", one);

  expect(await pages.readEntry("/a")).toBeNull();
  expect(await useCache.readEntry("key")).toEqual(useCacheEntry);
  expect(await pages.readEntry("/b")).toEqual(one);
});
