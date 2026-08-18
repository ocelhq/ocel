import { expect, test } from "vitest";
import { boundCacheTags } from "../src/cache-tags.mjs";

test("keeps tags in the order they were produced", () => {
  expect(boundCacheTags(["products", "_N_T_/layout"])).toEqual({
    tags: ["products", "_N_T_/layout"],
    dropped: 0,
  });
});

test("percent-encodes the whitespace a header cannot carry", () => {
  expect(boundCacheTags(["two words\tmore"]).tags).toEqual(["two%20words%09more"]);
});

test("drops an empty tag and one over the per-tag budget", () => {
  const long = "x".repeat(1025);
  expect(boundCacheTags(["", long, "products"])).toEqual({
    tags: ["products"],
    dropped: 2,
  });
});

test("collapses a tag two entries both named", () => {
  expect(boundCacheTags(["products", "products"])).toEqual({
    tags: ["products"],
    dropped: 0,
  });
});

test("stops at the tag count budget and counts the rest as dropped", () => {
  const raw = Array.from({ length: 1002 }, (_, i) => `t${i}`);
  const { tags, dropped } = boundCacheTags(raw);
  expect(tags).toHaveLength(1000);
  expect(dropped).toBe(2);
});

test("stops at the total byte budget", () => {
  const raw = Array.from({ length: 100 }, (_, i) => `${i}`.padStart(4, "0").repeat(64));
  const { tags, dropped } = boundCacheTags(raw);
  expect(tags.join(",").length).toBeLessThanOrEqual(16 * 1024);
  expect(tags.length + dropped).toBe(100);
});
