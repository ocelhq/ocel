import { describe, expect, it } from "bun:test";
import {
  CACHE_HEADER,
  cacheControlFor,
  DYNAMIC_CACHE_CONTROL,
  imageCacheControl,
  IMMUTABLE_CACHE_CONTROL,
  ROUTER_VARY,
  SERVED_CACHE_CONTROL,
  tierOf,
  variesOn,
} from "./cacheHeaders";

function answered(headers: Record<string, string>): Response {
  return new Response(null, { headers });
}

describe("cacheControlFor", () => {
  it("spells a page that never revalidates as the served literal", () => {
    expect(cacheControlFor(false)).toBe(SERVED_CACHE_CONTROL);
  });

  it("spells a page with any revalidate as the same served literal", () => {
    expect(cacheControlFor(5)).toBe(SERVED_CACHE_CONTROL);
    expect(cacheControlFor(3600)).toBe(SERVED_CACHE_CONTROL);
  });

  it("reads a zero revalidate as the dynamic literal", () => {
    expect(cacheControlFor(0)).toBe(DYNAMIC_CACHE_CONTROL);
  });
});

describe("SERVED_CACHE_CONTROL", () => {
  it("is a max-age of zero the client must revalidate", () => {
    expect(SERVED_CACHE_CONTROL).toBe("public, max-age=0, must-revalidate");
  });
});

describe("imageCacheControl", () => {
  it("spells the optimizer's ttl as a public max-age that must be revalidated", () => {
    expect(imageCacheControl(60)).toBe("public, max-age=60, must-revalidate");
  });
});

describe("IMMUTABLE_CACHE_CONTROL", () => {
  it("is a year of max-age", () => {
    expect(IMMUTABLE_CACHE_CONTROL).toBe("public, max-age=31536000, immutable");
  });
});

describe("variesOn", () => {
  it("ignores case, spacing and anything the response varies on besides", () => {
    expect(
      variesOn(
        "rsc, next-router-state-tree,next-router-prefetch , Next-Router-Segment-Prefetch, Accept-Encoding",
        ROUTER_VARY,
      ),
    ).toBe(true);
  });

  it("is false when one of the names is missing or nothing varies at all", () => {
    expect(variesOn("rsc, next-router-state-tree, next-router-prefetch", ROUTER_VARY)).toBe(false);
    expect(variesOn(null, ROUTER_VARY)).toBe(false);
  });
});

describe("tierOf", () => {
  it("reads the tier the edge stamped", () => {
    expect(tierOf(answered({ [CACHE_HEADER]: "PRERENDER" }))).toBe("PRERENDER");
  });

  it("refuses a response with no tier, and a tier this contract does not know", () => {
    expect(() => tierOf(answered({}))).toThrow(CACHE_HEADER);
    expect(() => tierOf(answered({ [CACHE_HEADER]: "REVALIDATED" }))).toThrow("REVALIDATED");
  });
});
