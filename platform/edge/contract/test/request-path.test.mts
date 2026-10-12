import { describe, expect, it } from "vitest";

import {
  encodeForwardedSearch,
  needsSlashNormalization,
  normalizeRepeatedSlashes,
  readPathAndQuery,
  readRawPath,
} from "../src/request-path.mjs";

describe("readPathAndQuery", () => {
  it.each([
    ["https://shop.example/a//b?x=1", "/a//b?x=1"],
    ["http://127.0.0.1:8080/", "/"],
    ["https://shop.example", ""],
    ["HTTPS://Shop.Example/a?x=//", "/a?x=//"],
  ])("takes %s down to %s", (url, expected) => {
    expect(readPathAndQuery(url)).toBe(expected);
  });
});

describe("readRawPath", () => {
  it.each([
    ["https://shop.example/a//b?x=1", "/a//b"],
    ["https://shop.example/a//b", "/a//b"],
    ["https://shop.example/a\\b?x=1", "/a\\b"],
    ["https://shop.example/?x=1", "/"],
  ])("takes %s down to the path %s, as the client wrote it", (url, expected) => {
    expect(readRawPath(url)).toBe(expected);
  });
});

describe("encodeForwardedSearch", () => {
  it("encodes a second question mark so the function URL keeps it in the query", () => {
    expect(encodeForwardedSearch("?a=1?b=2")).toBe("?a=1%3Fb=2");
  });

  it("leaves a search with one question mark, or none, alone", () => {
    expect(encodeForwardedSearch("?a=1")).toBe("?a=1");
    expect(encodeForwardedSearch("")).toBe("");
  });
});

describe("needsSlashNormalization / normalizeRepeatedSlashes", () => {
  it.each(["/a//b", "/a///b", "/basepath//en/x", "/a\\b", "/a/\\b"])(
    "flags %s as needing normalization",
    (path) => {
      expect(needsSlashNormalization(path)).toBe(true);
    },
  );

  it.each(["/a", "/a/b", "/basepath/en/x", "/"])("leaves %s alone", (path) => {
    expect(needsSlashNormalization(path)).toBe(false);
  });

  it.each([
    ["/a//b", "/a/b"],
    ["/a///b", "/a/b"],
    ["/basepath//en/x", "/basepath/en/x"],
    ["/a\\b", "/a/b"],
    ["/a//b?x=1//2", "/a/b?x=1//2"],
    ["/a//b?x=1//2?y=3", "/a/b?x=1//2?y=3"],
    ["/a//b/", "/a/b/"],
    ["//", "/"],
  ])("normalizes %s to %s", (input, expected) => {
    expect(normalizeRepeatedSlashes(input)).toBe(expected);
  });
});
