import { expect, test } from "vitest";
import { instanceCacheBytes, newInstanceCache } from "../src/instance-cache.mjs";

const MB = 1024 * 1024;

test("evicts the least recently used entry once the byte budget is exceeded", () => {
  const cache = newInstanceCache(300);

  cache.write("a", "A", 100);
  cache.write("b", "B", 100);
  cache.write("c", "C", 100);
  cache.write("d", "D", 100);

  expect(cache.read("a")).toBeUndefined();
  expect(cache.read("b")).toBe("B");
  expect(cache.read("d")).toBe("D");
});

test("a read refreshes the entry's recency", () => {
  const cache = newInstanceCache(300);

  cache.write("a", "A", 100);
  cache.write("b", "B", 100);
  cache.write("c", "C", 100);
  cache.read("a");
  cache.write("d", "D", 100);

  expect(cache.read("b")).toBeUndefined();
  expect(cache.read("a")).toBe("A");
});

test("refuses an entry larger than the budget without evicting anything", () => {
  const cache = newInstanceCache(300);

  cache.write("small", "S", 100);
  cache.write("huge", "H", 301);

  expect(cache.read("huge")).toBeUndefined();
  expect(cache.read("small")).toBe("S");
});

test("replacing a key counts only the new entry's bytes", () => {
  const cache = newInstanceCache(300);

  cache.write("a", "A1", 200);
  cache.write("a", "A2", 100);
  cache.write("b", "B", 200);

  expect(cache.read("a")).toBe("A2");
  expect(cache.read("b")).toBe("B");
});

test("misses a key that was never written", () => {
  expect(newInstanceCache(300).read("absent")).toBeUndefined();
});

test("two caches share no entries", () => {
  const first = newInstanceCache(300);
  const second = newInstanceCache(300);

  first.write("a", "A", 10);

  expect(second.read("a")).toBeUndefined();
});

test("the byte budget is a tenth of the memory given", () => {
  expect(instanceCacheBytes(2048 * MB)).toBe(214748364);
  expect(instanceCacheBytes(1000)).toBe(100);
});

test("the byte budget is 50 MiB when no memory is given", () => {
  expect(instanceCacheBytes()).toBe(52428800);
  expect(instanceCacheBytes(0)).toBe(52428800);
});

test("an entry too large to keep drops the value it would have replaced", () => {
  const cache = newInstanceCache(300);

  cache.write("a", "old", 100);
  cache.write("a", "new", 400);

  expect(cache.read("a")).toBeUndefined();
});
