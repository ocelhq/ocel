import { describe, expect, it } from "vitest";

import { shouldBypass } from "../src/index";

const check = (
  config: Parameters<typeof shouldBypass>[2],
  url: string,
  headers: Record<string, string> = {},
) => {
  const request = new Request(url, { headers });
  return shouldBypass(request, new URL(url), config);
};

// The three conditions Next actually emits for an app-router prerender.
const nextBypassFor = [
  { type: "header", key: "next-action" },
  { type: "header", key: "content-type", value: "multipart/form-data;.*" },
  { type: "header", key: "user-agent", value: "Bingbot|Slackbot|Twitterbot" },
] as Parameters<typeof shouldBypass>[2]["bypassFor"];

describe("shouldBypass", () => {
  it("matches a valued condition as an anchored regex, not string equality", () => {
    expect(
      check({ bypassFor: nextBypassFor }, "https://app.example/", {
        "content-type": "multipart/form-data; boundary=--abc",
      }),
    ).toBe(true);
  });

  it("anchors the regex at both ends", () => {
    expect(
      check(
        { bypassFor: [{ type: "header", key: "x-k", value: "abc" }] },
        "https://app.example/",
        { "x-k": "xxabcxx" },
      ),
    ).toBe(false);
  });

  it("ORs the conditions rather than ANDing them", () => {
    // Only the bot UA matches; the other two conditions are absent.
    expect(
      check({ bypassFor: nextBypassFor }, "https://app.example/", {
        "user-agent": "Slackbot",
      }),
    ).toBe(true);
  });

  it("matches a bare condition on presence of a truthy value", () => {
    expect(
      check({ bypassFor: nextBypassFor }, "https://app.example/", {
        "next-action": "7f3a",
      }),
    ).toBe(true);
    expect(check({ bypassFor: nextBypassFor }, "https://app.example/")).toBe(
      false,
    );
  });

  it("treats an empty header value as absent, as Next does", () => {
    expect(
      check({ bypassFor: nextBypassFor }, "https://app.example/", {
        "next-action": "",
      }),
    ).toBe(false);
  });

  it("strips the port and lowercases the host", () => {
    const config = {
      bypassFor: [{ type: "host" as const, value: "preview.example" }],
    };
    expect(check(config, "https://PREVIEW.example:8787/")).toBe(true);
    expect(check(config, "https://app.example:8787/")).toBe(false);
  });

  it("matches a cookie by name, ignoring a valueless key prefix", () => {
    const config = { bypassFor: [{ type: "cookie" as const, key: "badcooki" }] };
    expect(check(config, "https://app.example/", { cookie: "badcookie" })).toBe(
      false,
    );
    expect(
      check(config, "https://app.example/", { cookie: "badcooki=1" }),
    ).toBe(true);
  });

  it("matches a query condition on the last value of a repeated key", () => {
    const config = {
      bypassFor: [{ type: "query" as const, key: "mode", value: "draft" }],
    };
    expect(check(config, "https://app.example/?mode=live&mode=draft")).toBe(
      true,
    );
    expect(check(config, "https://app.example/?mode=draft&mode=live")).toBe(
      false,
    );
  });

  it("bypasses on a matching revalidate token", () => {
    const config = { bypassToken: "tok" };
    expect(
      check(config, "https://app.example/", { "x-prerender-revalidate": "tok" }),
    ).toBe(true);
    expect(
      check(config, "https://app.example/", {
        "x-prerender-revalidate": "wrong",
      }),
    ).toBe(false);
  });

  it("does not bypass a plain request", () => {
    expect(check({ bypassFor: nextBypassFor, bypassToken: "tok" }, "https://app.example/")).toBe(
      false,
    );
  });

  it("survives a malformed condition regex without throwing", () => {
    expect(
      check(
        { bypassFor: [{ type: "header", key: "x-k", value: "([" }] },
        "https://app.example/",
        { "x-k": "([" },
      ),
    ).toBe(false);
  });
});
