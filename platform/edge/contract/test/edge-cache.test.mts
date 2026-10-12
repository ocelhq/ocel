import { expect, test } from "vitest";
import { cachesOnlyMarked, readEdgeCacheMode } from "../src/edge-cache.mjs";

test("an origin with no edge cache variable has no edge that caches its responses", () => {
  expect(readEdgeCacheMode({})).toBeUndefined();
  expect(readEdgeCacheMode({ OCEL_EDGE_CACHE: "" })).toBeUndefined();
});

test("an origin is told whether its edge caches only the requests it marks or every response", () => {
  expect(readEdgeCacheMode({ OCEL_EDGE_CACHE: "marked" })).toBe("marked");
  expect(readEdgeCacheMode({ OCEL_EDGE_CACHE: "all" })).toBe("all");
});

test("an edge cache mode the origin does not know stops it at boot", () => {
  expect(() => readEdgeCacheMode({ OCEL_EDGE_CACHE: "1" })).toThrow(/OCEL_EDGE_CACHE is 1/);
});

test("a mode named none is not a mode: an origin with no caching edge is given no variable", () => {
  expect(() => readEdgeCacheMode({ OCEL_EDGE_CACHE: "none" })).toThrow(/OCEL_EDGE_CACHE is none/);
});

test("only an edge that caches the requests it marks caches only marked requests", () => {
  expect(cachesOnlyMarked("marked")).toBe(true);
  expect(cachesOnlyMarked("all")).toBe(false);
  expect(cachesOnlyMarked(undefined)).toBe(false);
});
