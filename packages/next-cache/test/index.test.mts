import { describe, expect, it } from "vitest";

import {
  areTagsExpired,
  base64ToBytes,
  bytesToBase64,
  cacheKey,
  deserialize,
  entryObjectKey,
  tagsOf,
  type TagRecord,
} from "../src/index.mjs";

describe("cacheKey", () => {
  it("maps root and empty to index", () => {
    expect(cacheKey("/")).toBe("index");
    expect(cacheKey("")).toBe("index");
  });

  it("strips the leading slash", () => {
    expect(cacheKey("/blog/a")).toBe("blog/a");
  });
});

describe("entryObjectKey", () => {
  const PREFIX = "prod/proj/web/BUILD1";

  it("roots the key under the deploy's own cache slice", () => {
    expect(entryObjectKey(PREFIX, cacheKey("/blog/post"))).toBe(
      "prod/proj/web/BUILD1/cache/blog/post.cache.json",
    );
  });

  it("keys a trailing-slash route, which the direct write path also accepted", () => {
    expect(entryObjectKey(PREFIX, cacheKey("/blog/"))).toBe(
      "prod/proj/web/BUILD1/cache/blog/.cache.json",
    );
    expect(entryObjectKey(PREFIX, "blog//post")).toBe(
      "prod/proj/web/BUILD1/cache/blog//post.cache.json",
    );
  });

  it("refuses a key that could climb out of the prefix", () => {
    expect(entryObjectKey(PREFIX, "../other/entry")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog/../../escape")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog/./post")).toBeNull();
    expect(entryObjectKey(PREFIX, "/absolute")).toBeNull();
    expect(entryObjectKey(PREFIX, "blog\\post")).toBeNull();
    expect(entryObjectKey(PREFIX, "")).toBeNull();
  });
});

describe("base64 codec", () => {
  it("round-trips arbitrary bytes", () => {
    const bytes = new Uint8Array([0, 1, 2, 253, 254, 255, 127, 128]);
    expect(base64ToBytes(bytesToBase64(bytes))).toEqual(bytes);
  });

  it("decodes what Node's Buffer produced", () => {
    const b64 = Buffer.from("RSC payload").toString("base64");
    expect(new TextDecoder().decode(base64ToBytes(b64))).toBe("RSC payload");
  });
});

describe("tagsOf", () => {
  it("splits the x-next-cache-tags header for page kinds", () => {
    expect(
      tagsOf({ kind: "APP_PAGE", headers: { "x-next-cache-tags": "a,b" } }, {}),
    ).toEqual(["a", "b"]);
  });

  it("returns no tags when the header is absent or empty", () => {
    expect(tagsOf({ kind: "APP_PAGE", headers: {} }, {})).toEqual([]);
    expect(
      tagsOf({ kind: "APP_PAGE", headers: { "x-next-cache-tags": "" } }, {}),
    ).toEqual([]);
  });

  it("combines ctx and value tags for FETCH kinds", () => {
    expect(
      tagsOf(
        { kind: "FETCH", tags: ["v"] },
        { tags: ["c"], softTags: ["s"] },
      ),
    ).toEqual(["c", "s", "v"]);
  });

  // The shape every stored fetch entry has: it records the tags it was written
  // under, and its reader passes the same ones back in — so this is the one call
  // that decides whether the answer is a dependency set or a bag.
  it("names a tag once when the entry and the request agree on it", () => {
    expect(
      tagsOf(
        { kind: "FETCH", tags: ["products"] },
        { tags: ["products"], softTags: ["_N_T_/shop"] },
      ),
    ).toEqual(["products", "_N_T_/shop"]);
  });
});

describe("areTagsExpired", () => {
  const records = (m: Record<string, TagRecord>) => new Map(Object.entries(m));

  it("expires when an expiry passed and landed after the entry", () => {
    expect(
      areTagsExpired(["t"], records({ t: { expired: 500 } }), 100, 1000),
    ).toBe(true);
  });

  it("does not expire an entry written after the expiry", () => {
    expect(
      areTagsExpired(["t"], records({ t: { expired: 500 } }), 1000, 2000),
    ).toBe(false);
  });

  it("does not expire when the expiry is still in the future", () => {
    expect(
      areTagsExpired(["t"], records({ t: { expired: 5000 } }), 100, 1000),
    ).toBe(false);
  });

  it("ignores tags with no record", () => {
    expect(areTagsExpired(["t"], records({}), 100, 1000)).toBe(false);
  });
});

describe("deserialize", () => {
  it("restores APP_ROUTE body as bytes", () => {
    const out = deserialize({
      kind: "APP_ROUTE",
      body: Buffer.from("hello").toString("base64"),
    });
    expect(out.body).toBeInstanceOf(Uint8Array);
    expect(new TextDecoder().decode(out.body as Uint8Array)).toBe("hello");
  });

  it("restores APP_PAGE rscData and segments as bytes, keeps html a string", () => {
    const out = deserialize({
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      rscData: Buffer.from("RSC").toString("base64"),
      segmentData: { "/_tree": Buffer.from("TREE").toString("base64") },
    });
    expect(out.html).toBe("<html>hi</html>");
    expect(new TextDecoder().decode(out.rscData as Uint8Array)).toBe("RSC");
    const segs = out.segmentData as Map<string, Uint8Array>;
    expect(new TextDecoder().decode(segs.get("/_tree")!)).toBe("TREE");
  });

  it("passes PAGES through with html intact", () => {
    const out = deserialize({
      kind: "PAGES",
      html: "<html>p</html>",
      pageData: { a: 1 },
    });
    expect(out.html).toBe("<html>p</html>");
    expect(out.pageData).toEqual({ a: 1 });
  });
});
