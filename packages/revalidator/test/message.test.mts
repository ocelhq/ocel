import { describe, expect, it } from "vitest";

import { parseMessage, permittedHosts } from "../src/message.mjs";

const host = "abc123.lambda-url.us-east-1.on.aws";
const permitted = permittedHosts(host);

function body(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    v: 1,
    url: `https://${host}/blog/post`,
    headers: { "x-prerender-revalidate": "secret", "x-ocel-entry": "app" },
    expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
    isrPrefix: "build-1",
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
    ...overrides,
  });
}

describe("permittedHosts", () => {
  it("reads a comma-separated list, trimming blanks and case", () => {
    expect(permittedHosts(" A.lambda-url.us-east-1.on.aws , b.lambda-url.eu-west-1.on.aws ,")).toEqual(
      new Set(["a.lambda-url.us-east-1.on.aws", "b.lambda-url.eu-west-1.on.aws"]),
    );
  });

  it("is empty when unset", () => {
    expect(permittedHosts(undefined)).toEqual(new Set());
  });
});

describe("parseMessage", () => {
  it("accepts a well-formed message on a permitted host", () => {
    const result = parseMessage(body(), permitted);
    expect(result).toEqual({
      ok: true,
      message: {
        v: 1,
        url: `https://${host}/blog/post`,
        headers: { "x-prerender-revalidate": "secret", "x-ocel-entry": "app" },
        expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
        isrPrefix: "build-1",
        routePath: "/blog/post",
        lastModified: 1_700_000_000_000,
        enqueuedAt: 1_700_000_000_500,
      },
    });
  });

  it("accepts a message declaring no expectation", () => {
    const result = parseMessage(body({ expect: null }), permitted);
    expect(result.ok && result.message.expect).toBeNull();
  });

  it("rejects an unknown version", () => {
    expect(parseMessage(body({ v: 2 }), permitted)).toEqual({ ok: false, reason: "unsupported-version" });
  });

  it("rejects a body that is not JSON", () => {
    expect(parseMessage("{", permitted)).toEqual({ ok: false, reason: "malformed" });
  });

  it("rejects a message missing a dedup ingredient", () => {
    expect(parseMessage(body({ lastModified: "1700000000000" }), permitted)).toEqual({
      ok: false,
      reason: "malformed",
    });
  });

  it("rejects a header map holding a non-string value", () => {
    expect(parseMessage(body({ headers: { "x-ocel-entry": 7 } }), permitted)).toEqual({
      ok: false,
      reason: "malformed",
    });
  });

  it("rejects a host outside the permitted set", () => {
    expect(parseMessage(body({ url: "https://attacker.lambda-url.us-east-1.on.aws/x" }), permitted)).toEqual({
      ok: false,
      reason: "host-not-permitted",
    });
  });

  it("rejects a permitted host reached over http", () => {
    expect(parseMessage(body({ url: `http://${host}/blog/post` }), permitted)).toEqual({
      ok: false,
      reason: "host-not-permitted",
    });
  });

  it("rejects a permitted host carrying an explicit port", () => {
    expect(parseMessage(body({ url: `https://${host}:8443/blog/post` }), permitted)).toEqual({
      ok: false,
      reason: "host-not-permitted",
    });
  });

  it("rejects every host when nothing is permitted", () => {
    expect(parseMessage(body(), permittedHosts(""))).toEqual({ ok: false, reason: "host-not-permitted" });
  });
});
