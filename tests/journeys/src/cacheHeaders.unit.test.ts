import { describe, expect, it } from "vitest";
import {
  CACHE_HEADER,
  cacheControlFor,
  DYNAMIC_CACHE_CONTROL,
  ROUTER_VARY,
  tierOf,
  variesOn,
} from "./cacheHeaders";

function answered(headers: Record<string, string>): Response {
  return new Response(null, { headers });
}

describe("cacheControlFor", () => {
  it("spells a page that never revalidates as a year of s-maxage alone", () => {
    expect(cacheControlFor(false)).toBe("s-maxage=31536000");
  });

  it("gives the rest of the year to stale-while-revalidate", () => {
    expect(cacheControlFor(5)).toBe("s-maxage=5, stale-while-revalidate=31535995");
    expect(cacheControlFor(3600)).toBe("s-maxage=3600, stale-while-revalidate=31532400");
  });

  it("reads a zero revalidate as the dynamic literal", () => {
    expect(cacheControlFor(0)).toBe(DYNAMIC_CACHE_CONTROL);
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
