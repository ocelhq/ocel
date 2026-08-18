import { describe, expect, it } from "vitest";

import { invalidationBatches, pathsPerInvalidation } from "../src/tags.mjs";

const RELEASE = "r0a1b2c3d";

describe("invalidationBatches", () => {
  it("prefixes every tag with the release and the tag marker", () => {
    expect(invalidationBatches(RELEASE, ["products", "cart"]).batches).toEqual([
      [`#${RELEASE}|products`, `#${RELEASE}|cart`],
    ]);
  });

  it("puts soft tags first, mirroring the order the origin stored them in", () => {
    expect(
      invalidationBatches(RELEASE, ["products", "_N_T_/shop", "cart", "_N_T_/"]).batches,
    ).toEqual([
      [
        `#${RELEASE}|_N_T_/shop`,
        `#${RELEASE}|_N_T_/`,
        `#${RELEASE}|products`,
        `#${RELEASE}|cart`,
      ],
    ]);
  });

  it("splits into batches of at most the paths one request carries", () => {
    const tags = Array.from({ length: pathsPerInvalidation * 2 + 1 }, (_, i) => `t${i}`);
    const { batches } = invalidationBatches(RELEASE, tags);

    expect(batches.map((batch) => batch.length)).toEqual([
      pathsPerInvalidation,
      pathsPerInvalidation,
      1,
    ]);
    expect(batches.flat()).toEqual(tags.map((tag) => `#${RELEASE}|${tag}`));
  });

  it("sends every raised tag rather than the fifty one object holds", () => {
    const tags = Array.from({ length: 60 }, (_, i) => `t${i}`);

    expect(invalidationBatches(RELEASE, tags).batches.flat()).toHaveLength(60);
  });

  it("names the tags CloudFront never stored rather than spending a batch on them", () => {
    const long = "x".repeat(256);
    const { batches, dropped } = invalidationBatches(RELEASE, [
      "with space",
      "with,comma",
      "with\ttab",
      "",
      "é",
      long,
      "kept",
    ]);

    expect(batches).toEqual([[`#${RELEASE}|kept`]]);
    expect(dropped).toEqual(["with space", "with,comma", "with\ttab", "é", long]);
  });

  it("has nothing to send when every tag is unstorable", () => {
    expect(invalidationBatches(RELEASE, ["with space"]).batches).toEqual([]);
  });

  it("counts the release stamp against the tag CloudFront can hold", () => {
    const fits = "x".repeat(256 - RELEASE.length - 1);

    expect(invalidationBatches(RELEASE, [fits]).batches).toEqual([[`#${RELEASE}|${fits}`]]);
    expect(invalidationBatches(RELEASE, [fits + "x"]).batches).toEqual([]);
  });
});
