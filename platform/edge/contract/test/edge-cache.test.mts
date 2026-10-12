import { expect, test } from "vitest";
import { readEdgeCacheMode } from "../src/edge-cache.mjs";

test("an origin with no edge cache variable has no edge that caches its responses", () => {
  expect(readEdgeCacheMode({})).toBe("none");
  expect(readEdgeCacheMode({ OCEL_EDGE_CACHE: "" })).toBe("none");
});

test("an origin is told whether its edge caches only the requests it marks or every response", () => {
  expect(readEdgeCacheMode({ OCEL_EDGE_CACHE: "marked" })).toBe("marked");
  expect(readEdgeCacheMode({ OCEL_EDGE_CACHE: "all" })).toBe("all");
});

test("an edge cache mode the origin does not know stops it at boot", () => {
  expect(() => readEdgeCacheMode({ OCEL_EDGE_CACHE: "1" })).toThrow(/OCEL_EDGE_CACHE is 1/);
});
