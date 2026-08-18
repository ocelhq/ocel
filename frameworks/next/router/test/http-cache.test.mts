import { describe, expect, it } from "vitest";

import { storagePolicy, withStatus, withVercelCacheAlias } from "../src/http-cache.mjs";

describe("storagePolicy", () => {
  it("reads a bare s-maxage as a zero-swr policy", () => {
    expect(storagePolicy("s-maxage=31536000")).toEqual({
      sMaxAge: 31536000,
      swr: 0,
    });
  });

  it("reads s-maxage plus stale-while-revalidate", () => {
    expect(storagePolicy("s-maxage=60, stale-while-revalidate=30")).toEqual({
      sMaxAge: 60,
      swr: 30,
    });
  });

  it("refuses to store private / no-store / no-cache responses", () => {
    expect(
      storagePolicy("private, no-cache, no-store, max-age=0, must-revalidate"),
    ).toBeNull();
  });

  it("refuses responses with no positive s-maxage", () => {
    expect(storagePolicy("max-age=0")).toBeNull();
    expect(storagePolicy("s-maxage=0")).toBeNull();
    expect(storagePolicy(null)).toBeNull();
  });
});

describe("withVercelCacheAlias", () => {
  it("returns the response untouched when the build did not opt in", () => {
    const response = withStatus(new Response("body"), "HIT");

    const aliased = withVercelCacheAlias(response, undefined);

    expect(aliased).toBe(response);
    expect(aliased.headers.get("x-vercel-cache")).toBeNull();
  });

  it("emits no alias for a response no tier stamped a status on", () => {
    const response = new Response("body");

    const aliased = withVercelCacheAlias(response, true);

    expect(aliased).toBe(response);
    expect(aliased.headers.get("x-vercel-cache")).toBeNull();
  });

  it("copies the status verbatim, body and headers intact", async () => {
    const response = withStatus(
      new Response("body", { headers: { "content-type": "text/plain" } }),
      "STALE",
    );

    const aliased = withVercelCacheAlias(response, true);

    expect(aliased.headers.get("x-vercel-cache")).toBe("STALE");
    expect(aliased.headers.get("x-ocel-cache")).toBe("STALE");
    expect(aliased.headers.get("content-type")).toBe("text/plain");
    expect(await aliased.text()).toBe("body");
  });

  it("leaves the response the caller may still store unaliased", () => {
    const stored = withStatus(new Response("body"), "PRERENDER");

    withVercelCacheAlias(stored, true);

    expect(stored.headers.get("x-vercel-cache")).toBeNull();
  });
});
