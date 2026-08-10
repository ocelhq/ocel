import { describe, expect, it } from "vitest";

import { isrPrefixOf, tagNamespace } from "../src/index.mjs";

const SEGMENT = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-";

function segment(seed: number): string {
  let out = "";
  let n = seed;
  for (let i = 0; i < 1 + (seed % 12); i++) {
    out += SEGMENT[n % SEGMENT.length];
    n = (n * 1103515245 + 12345) >>> 8;
  }
  return `p${out}`;
}

describe("isrPrefixOf", () => {
  it("round-trips every prefix tagNamespace can be given", () => {
    for (let seed = 0; seed < 512; seed++) {
      const prefix = [seed, seed * 7 + 1, seed * 13 + 2, seed * 31 + 3]
        .map(segment)
        .join("/");
      expect(isrPrefixOf(tagNamespace(prefix))).toBe(prefix);
    }
  });

  it("round-trips the shape the deploy actually produces", () => {
    expect(tagNamespace("prod/acme/web/BUILD1")).toBe("TAG#prod#acme#web#BUILD1#");
    expect(isrPrefixOf("TAG#prod#acme#web#BUILD1#")).toBe("prod/acme/web/BUILD1");
  });

  it("refuses a partition that is not a tag namespace", () => {
    expect(isrPrefixOf("UPLOAD#abc")).toBeNull();
    expect(isrPrefixOf("")).toBeNull();
    expect(isrPrefixOf("TAG#")).toBeNull();
  });

  it("refuses a namespace that is not exactly four segments", () => {
    expect(isrPrefixOf("TAG#prod#acme#web#")).toBeNull();
    expect(isrPrefixOf("TAG#prod#acme#web#BUILD1#extra#")).toBeNull();
    expect(isrPrefixOf("TAG#prod#acme##BUILD1#")).toBeNull();
  });

  it("refuses a whole tag partition key, which carries the tag as well", () => {
    expect(isrPrefixOf(tagNamespace("prod/acme/web/BUILD1") + "cart#42")).toBeNull();
  });
});
