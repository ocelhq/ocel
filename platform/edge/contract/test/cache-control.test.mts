import { expect, test } from "vitest";
import { isStoredBySomeEdge, readCacheDirectives } from "../src/cache-control.mjs";

test("reads directive names in lower case, and their values without quotes", () => {
  expect([...readCacheDirectives('Public, S-MaxAge=60, no-cache="set-cookie"')]).toEqual([
    ["public", true],
    ["s-maxage", "60"],
    ["no-cache", "set-cookie"],
  ]);
});

test.each([
  ["a max-age", "public, max-age=60", true],
  ["an s-maxage", "s-maxage=60", true],
  ["a zero lifetime it may serve stale", "max-age=0, stale-while-revalidate=30", true],
  ["a zero lifetime", "max-age=0", false],
  ["an s-maxage of zero over a max-age", "s-maxage=0, max-age=60", false],
  ["no lifetime", "public", false],
  ["a private response", "private, max-age=60", false],
  ["a no-store response", "no-store, s-maxage=60", false],
])("whether an edge stores a response with %s is %s", (_name, header, stored) => {
  expect(isStoredBySomeEdge(readCacheDirectives(header))).toBe(stored);
});
