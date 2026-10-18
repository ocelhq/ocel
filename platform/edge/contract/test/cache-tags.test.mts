import { expect, test } from "vitest";
import {
  boundCacheTags,
  buildPathCacheTag,
  readTagRelease,
  stampTag,
  storedCacheTags,
} from "../src/cache-tags.mjs";

test("reads back the release a stamped tag was stamped with, and a bare release as itself", () => {
  expect(readTagRelease(stampTag("r0a1b2c3d", "path:/a|b"))).toBe("r0a1b2c3d");
  expect(readTagRelease("r0a1b2c3d")).toBe("r0a1b2c3d");
});

test("names a path by its percent-encoded pathname, however the path was written", () => {
  expect(buildPathCacheTag("/café")).toBe("path:/caf%C3%A9");
  expect(buildPathCacheTag("/caf%C3%A9")).toBe("path:/caf%C3%A9");
  expect(buildPathCacheTag("/blog/a")).toBe("path:/blog/a");
});

test("stores a path tag with its percent escapes as it is", () => {
  expect(storedCacheTags("r0a1b2c3d", [buildPathCacheTag("/café")])).toEqual({
    tags: ["r0a1b2c3d|path:/caf%C3%A9"],
    unstorable: [],
    overflowed: [],
  });
});

test("stores the tag of a path with a comma, written either way", () => {
  expect(buildPathCacheTag("/a,b")).toBe("path:/a%2Cb");
  expect(buildPathCacheTag("/a%2Cb")).toBe("path:/a%2Cb");
  expect(storedCacheTags("r0a1b2c3d", [buildPathCacheTag("/a,b")]).tags).toEqual([
    "r0a1b2c3d|path:/a%2Cb",
  ]);
});

test("names a path that starts with two slashes by that path, not by a host", () => {
  expect(buildPathCacheTag("//evil.example/x")).toBe("path://evil.example/x");
});

test("refuses to store a tag that names a percent escape but no path", () => {
  expect(storedCacheTags("r0a1b2c3d", ["a%20b", "a b"]).unstorable).toEqual(["a%20b", "a b"]);
});

const generous = { tagBytes: 1024, tags: 1000, headerBytes: 16 * 1024 };

test("keeps tags in the order they were produced", () => {
  expect(boundCacheTags(["products", "_N_T_/layout"], generous)).toEqual({
    tags: ["products", "_N_T_/layout"],
    dropped: 0,
  });
});

test("percent-encodes the whitespace a header cannot contain", () => {
  expect(boundCacheTags(["two words\tmore"], generous).tags).toEqual(["two%20words%09more"]);
});

test("drops an empty tag and one over the per-tag limit it is handed", () => {
  expect(boundCacheTags(["", "abcde", "abcd"], { ...generous, tagBytes: 4 })).toEqual({
    tags: ["abcd"],
    dropped: 2,
  });
});

test("collapses a tag two entries both named", () => {
  expect(boundCacheTags(["products", "products"], generous)).toEqual({
    tags: ["products"],
    dropped: 0,
  });
});

test("stops at the tag count it is handed and counts the rest as dropped", () => {
  expect(boundCacheTags(["a", "b", "c", "d"], { ...generous, tags: 2 })).toEqual({
    tags: ["a", "b"],
    dropped: 2,
  });
});

test("stops at the header bytes it is handed, counting the commas between tags", () => {
  expect(boundCacheTags(["aaaa", "bbbb", "c"], { ...generous, headerBytes: 9 })).toEqual({
    tags: ["aaaa", "bbbb"],
    dropped: 1,
  });
});
