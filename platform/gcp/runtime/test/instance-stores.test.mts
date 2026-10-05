import type { CacheEntryFile } from "@framework/next-cache";
import { expect, test } from "vitest";
import { newInstanceCacheStore, newInstanceUseCacheStore } from "../src/next/instance-stores.mjs";

function page(html: string, lastModified = 1_000): CacheEntryFile {
  return { lastModified, value: { kind: "PAGES", html, pageData: {}, status: 200, headers: {} } };
}

test("an instance cache store reads back the entry it was handed", async () => {
  const store = newInstanceCacheStore(1024 * 1024);

  await store.writeEntry("/blog", page("<p>blog</p>"));

  expect(await store.readEntry("/blog")).toEqual(page("<p>blog</p>"));
  expect(await store.readEntry("/about")).toBeNull();
});

test("an instance cache store keeps fetch entries apart from page entries of the same key", async () => {
  const store = newInstanceCacheStore(1024 * 1024);

  await store.writeEntry("same", page("<p>page</p>"));
  await store.writeFetch("same", page("fetched"));

  expect(await store.readEntry("same")).toEqual(page("<p>page</p>"));
  expect(await store.readFetch("same")).toEqual(page("fetched"));
});

test("an instance cache store drops the least recently read entry once it outgrows its budget", async () => {
  const one = page("x".repeat(400));
  const budget = 2 * JSON.stringify(one).length + 10;
  const store = newInstanceCacheStore(budget);

  await store.writeEntry("/a", one);
  await store.writeEntry("/b", one);
  await store.readEntry("/a");
  await store.writeEntry("/c", one);

  expect(await store.readEntry("/a")).not.toBeNull();
  expect(await store.readEntry("/b")).toBeNull();
  expect(await store.readEntry("/c")).not.toBeNull();
});

test("an instance cache store keeps no entry larger than its whole budget", async () => {
  const store = newInstanceCacheStore(100);

  await store.writeEntry("/huge", page("x".repeat(1_000)));

  expect(await store.readEntry("/huge")).toBeNull();
});

test("an instance cache store takes tag writes, which the instance's own tag clock already holds", async () => {
  await expect(
    newInstanceCacheStore(1024).writeTags(["products"], { expired: 1 }),
  ).resolves.toBeUndefined();
});

test("an instance use-cache store reads back the entry it was handed", async () => {
  const store = newInstanceUseCacheStore(1024 * 1024);
  const entry = { tags: ["a"], stale: 1, timestamp: 2, expire: 3, revalidate: 4, body: "Ym9keQ==" };

  await store.writeEntry("key", entry);

  expect(await store.readEntry("key")).toEqual(entry);
  expect(await store.readEntry("other")).toBeNull();
});

test("an instance use-cache store has a tag snapshot with no tags another instance wrote", async () => {
  const store = newInstanceUseCacheStore(1024);

  expect(await store.readTagSnapshot(null)).toEqual({ status: "fresh", records: {}, etag: null });
  expect(await store.writeTag("cart", { expired: 1, writtenAt: 1 })).toBe(true);
});
