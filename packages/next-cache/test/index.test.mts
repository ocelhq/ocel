import { describe, expect, it } from "vitest";

import {
  areTagsExpired,
  base64ToBytes,
  bytesToBase64,
  cacheKey,
  deserialize,
  tagsOf,
  type TagRecord,
} from "../src/index.mjs";

describe("cacheKey", () => {
  it("maps root and empty to index", () => {
    expect(cacheKey("/", undefined)).toBe("index");
    expect(cacheKey("", undefined)).toBe("index");
  });

  it("strips the leading slash", () => {
    expect(cacheKey("/blog/a", undefined)).toBe("blog/a");
  });

  it("namespaces FETCH under __fetch__/", () => {
    expect(cacheKey("/abc", "FETCH")).toBe("__fetch__/abc");
    expect(cacheKey("abc", "FETCH")).toBe("__fetch__/abc");
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
