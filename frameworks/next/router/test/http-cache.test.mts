import { describe, expect, it } from "vitest";

import { storagePolicy } from "../src/http-cache.mjs";

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
    expect(storagePolicy("private, no-cache, no-store, max-age=0, must-revalidate")).toBeNull();
  });

  it("refuses responses with no positive s-maxage", () => {
    expect(storagePolicy("max-age=0")).toBeNull();
    expect(storagePolicy("s-maxage=0")).toBeNull();
    expect(storagePolicy(null)).toBeNull();
  });
});
