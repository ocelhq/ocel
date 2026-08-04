import { describe, expect, it } from "vitest";

import { isrPrefixOf, tagNamespace } from "../src/index.mjs";

// The four segments of an isrPrefix, as workers/isr-writer spells them:
// <env>/<project>/<app>/<buildId>, each drawn from an alphabet that excludes
// the "#" the namespace joins them with.
const SEGMENT = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-";

function segment(seed: number): string {
  let out = "";
  // A leading alphanumeric, matching the writer's own segment grammar.
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
    // An upload session's item: same table, same sk, and its own key grammar.
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
    // The tag is arbitrary user input and freely contains "#", so a reader that
    // split a pk would derive a prefix from the caller's own bytes.
    expect(isrPrefixOf(tagNamespace("prod/acme/web/BUILD1") + "cart#42")).toBeNull();
  });
});
