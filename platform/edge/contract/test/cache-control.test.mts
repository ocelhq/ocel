import { expect, test } from "vitest";
import {
  isStoredBySomeEdge,
  listSharedPolicyHeaders,
  readCacheDirectives,
} from "../src/cache-control.mjs";

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
  ["a future Expires in RFC 850 form", { expires: "Monday, 12-Oct-26 12:01:00 GMT" }, true],
  ["a future Expires in asctime form", { expires: "Mon Oct 12 12:01:00 2026" }, true],
  ["a future asctime Expires with a one-digit day", { expires: "Tue Nov  3 12:01:00 2026" }, true],
  ["an Expires in RFC 850 form in the past", { expires: "Monday, 12-Oct-26 11:59:00 GMT" }, false],
  ["an Expires in asctime form in the past", { expires: "Mon Oct 12 11:59:00 2026" }, false],
  [
    "an RFC 850 Expires whose two-digit year is read as the past century",
    { expires: "Sunday, 06-Nov-94 08:49:37 GMT" },
    false,
  ],
  ["an Expires naming a day the month lacks", { expires: "Mon, 31 Feb 2027 12:00:00 GMT" }, false],
  [
    "an Expires that is not a date, which is already expired, with a stale-while-revalidate",
    { "cache-control": "stale-while-revalidate=30", expires: "0" },
    true,
  ],
  [
    "an Expires in the past with a stale-while-revalidate",
    { "cache-control": "stale-while-revalidate=30", expires: "Mon, 12 Oct 2026 11:59:00 GMT" },
    true,
  ],
  [
    "an Expires that is not a date under a max-age, which wins over it",
    { "cache-control": "max-age=60", expires: "0" },
    true,
  ],
  [
    "an Expires later than the Date it is stated against",
    { expires: "Mon, 12 Oct 2026 11:01:00 GMT", date: "Mon, 12 Oct 2026 11:00:00 GMT" },
    true,
  ],
] as const)("whether an edge stores a response with %s is %s", (_name, headers, stored) => {
  expect(isStoredBySomeEdge(new Headers(headers), now, undefined)).toBe(stored);
});

test("an edge stores a response for a lifetime in the cache-control header it names as its own", () => {
  const headers = new Headers({ "cloudflare-cdn-cache-control": "max-age=60" });
  expect(isStoredBySomeEdge(headers, now, "cloudflare-cdn-cache-control")).toBe(true);
});

test("no edge stores a response for a lifetime only another edge's header names", () => {
  const headers = new Headers({ "cloudflare-cdn-cache-control": "max-age=60" });
  expect(isStoredBySomeEdge(headers, now, undefined)).toBe(false);
  expect(isStoredBySomeEdge(headers, now, "cdn-cache-control")).toBe(false);
});

test("an edge's own header goes first, then CDN-Cache-Control, then Cache-Control", () => {
  expect(listSharedPolicyHeaders("Cloudflare-CDN-Cache-Control")).toEqual([
    "cloudflare-cdn-cache-control",
    "cdn-cache-control",
    "cache-control",
  ]);
  expect(listSharedPolicyHeaders("cdn-cache-control")).toEqual([
    "cdn-cache-control",
    "cache-control",
  ]);
  expect(listSharedPolicyHeaders(undefined)).toEqual(["cdn-cache-control", "cache-control"]);
});
