import { expect, it } from "vitest";

import { parseMessage } from "../src/revalidation.mjs";
import { body, isrPrefix, routeId } from "../src/revalidation-sample.mjs";

it("accepts a well-formed message", () => {
  expect(parseMessage(body())).toEqual({
    ok: true,
    message: {
      v: 1,
      headers: { "x-prerender-revalidate": "s3cr3t-preview-mode-id", "x-forwarded-host": "example.com" },
      expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
      isrPrefix,
      routeId,
      routePath: "/blog/post",
      lastModified: 1_700_000_000_000,
      enqueuedAt: 1_700_000_000_500,
    },
  });
});

it("names no host, and keeps no field that could carry one", () => {
  const parsed = parseMessage(body({ url: "https://attacker.lambda-url.us-east-1.on.aws/x", host: "attacker" }));

  expect(parsed.ok && Object.keys(parsed.message).sort()).toEqual([
    "enqueuedAt",
    "expect",
    "headers",
    "isrPrefix",
    "lastModified",
    "routeId",
    "routePath",
    "v",
  ]);
});

it("accepts a message declaring no expectation", () => {
  const result = parseMessage(body({ expect: null }));
  expect(result.ok && result.message.expect).toBeNull();
});

it("rejects an unknown version", () => {
  expect(parseMessage(body({ v: 2 }))).toEqual({ ok: false, reason: "unsupported-version" });
});

it("rejects a body that is not JSON", () => {
  expect(parseMessage("{")).toEqual({ ok: false, reason: "malformed" });
});

it("rejects a message missing a dedup ingredient", () => {
  expect(parseMessage(body({ lastModified: "1700000000000" }))).toEqual({ ok: false, reason: "malformed" });
});

it("rejects a message naming no route id", () => {
  expect(parseMessage(body({ routeId: undefined }))).toEqual({ ok: false, reason: "malformed" });
});

it("rejects a header map holding a non-string value", () => {
  expect(parseMessage(body({ headers: { "x-ocel-entry": 7 } }))).toEqual({ ok: false, reason: "malformed" });
});

it("rejects a route path that is not a path", () => {
  expect(parseMessage(body({ routePath: "https://attacker.example.com/x" }))).toEqual({
    ok: false,
    reason: "malformed",
  });
});

it.each([
  ["a fragment truncating the record name", "prod/proj/web/BID/fetch-cache/x.cache.json#"],
  ["a query truncating the record name", "prod/proj/web/BID/fetch-cache/x.cache.json?"],
  ["a prefix landing the record inside the edge's write region", "prod/proj/web/BID/fetch-cache"],
  ["a fetch-cache segment anywhere in the prefix", "prod/fetch-cache/web/BID"],
  ["a traversal out of the deploy's own prefix", "prod/proj/web/BID/../../../other-app/BID2"],
  ["a single-dot segment", "prod/./web/BID"],
  ["an absolute key", "/prod/proj/web/BID"],
  ["a trailing separator", "prod/proj/web/BID/"],
  ["an empty prefix", ""],
  ["a prefix that is not a key at all", "https://attacker.example.com/x"],
])("rejects an isrPrefix carrying %s", (_name, isrPrefix) => {
  expect(parseMessage(body({ isrPrefix }))).toEqual({ ok: false, reason: "malformed" });
});

it("accepts the isrPrefix shape the deploy actually builds", () => {
  const parsed = parseMessage(body({ isrPrefix: "preview.1/my-proj_2/web-app/BID-x_9.2" }));
  expect(parsed.ok && parsed.message.isrPrefix).toBe("preview.1/my-proj_2/web-app/BID-x_9.2");
});

it("rejects a header map holding a name that is not a token", () => {
  expect(parseMessage(body({ headers: { "bad header": "x" } }))).toEqual({ ok: false, reason: "malformed" });
});
