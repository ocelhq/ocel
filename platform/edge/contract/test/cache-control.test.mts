import { expect, test } from "vitest";
import { isStoredBySomeEdge, readCacheDirectives } from "../src/cache-control.mjs";

const now = Date.parse("Mon, 12 Oct 2026 12:00:00 GMT");

test("reads directive names in lower case, and their values without quotes", () => {
  expect([...readCacheDirectives('Public, S-MaxAge=60, no-cache="set-cookie"')]).toEqual([
    ["public", true],
    ["s-maxage", "60"],
    ["no-cache", "set-cookie"],
  ]);
});

test.each([
  ["a max-age", { "cache-control": "public, max-age=60" }, true],
  ["an s-maxage", { "cache-control": "s-maxage=60" }, true],
  [
    "a zero lifetime it may serve stale",
    { "cache-control": "max-age=0, stale-while-revalidate=30" },
    true,
  ],
  ["a zero lifetime", { "cache-control": "max-age=0" }, false],
  ["an s-maxage of zero over a max-age", { "cache-control": "s-maxage=0, max-age=60" }, false],
  ["no lifetime", { "cache-control": "public" }, false],
  ["a private response", { "cache-control": "private, max-age=60" }, false],
  ["a no-store response", { "cache-control": "no-store, s-maxage=60" }, false],
  ["a max-age in CDN-Cache-Control", { "cdn-cache-control": "max-age=60" }, true],
  [
    "a max-age in Cloudflare-CDN-Cache-Control",
    { "cloudflare-cdn-cache-control": "max-age=60" },
    true,
  ],
  ["no cache headers at all", {}, false],
  ["a future Expires and no Cache-Control", { expires: "Mon, 12 Oct 2026 12:01:00 GMT" }, true],
  [
    "a future Expires and a Cache-Control naming no lifetime",
    { "cache-control": "public", expires: "Mon, 12 Oct 2026 12:01:00 GMT" },
    true,
  ],
  [
    "a future Expires under a zero max-age, which wins over it",
    { "cache-control": "max-age=0", expires: "Mon, 12 Oct 2026 12:01:00 GMT" },
    false,
  ],
  [
    "a future Expires on a private response",
    { "cache-control": "private", expires: "Mon, 12 Oct 2026 12:01:00 GMT" },
    false,
  ],
  ["an Expires in the past", { expires: "Mon, 12 Oct 2026 11:59:00 GMT" }, false],
  ["an Expires that is not a date", { expires: "0" }, false],
  [
    "an Expires later than the Date it is stated against",
    { expires: "Mon, 12 Oct 2026 11:01:00 GMT", date: "Mon, 12 Oct 2026 11:00:00 GMT" },
    true,
  ],
] as const)("whether an edge stores a response with %s is %s", (_name, headers, stored) => {
  expect(isStoredBySomeEdge(new Headers(headers), now)).toBe(stored);
});
