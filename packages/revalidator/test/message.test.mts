import { expect, it } from "vitest";

import { parseMessage } from "../src/message.mjs";
import { body, isrPrefix, routeId } from "./fixture.mjs";

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

// The shape is the contract .25 builds against, and the security property is
// that it has no field naming a host: an edge that sends one is sending
// something the message does not carry.
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
