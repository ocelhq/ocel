import { describe, expect, it } from "vitest";
import { objectCall } from "../src/serve-key";

const key = "prod/shop/web/r1a2b3c4d/assets/_next/static/app.js";

describe("the cache key of an object read through Serve", () => {
  it("carries the host, app and release of the key it reads", () => {
    const call = objectCall("shop.example.com", key);
    expect(call.props).toEqual({
      kind: "object",
      host: "shop.example.com",
      app: "web",
      release: "r1a2b3c4d",
    });
    expect(new URL(call.url).pathname).toBe(`/${key}`);
  });

  it("keys the same path under another host apart", () => {
    const a = objectCall("a.example.com", key);
    const b = objectCall("b.example.com", key);
    expect(a.url).toBe(b.url);
    expect(a.props).not.toEqual(b.props);
  });

  it("keys the same file of another release apart", () => {
    const next = key.replace("r1a2b3c4d", "r5e6f7a8b");
    const a = objectCall("shop.example.com", key);
    const b = objectCall("shop.example.com", next);
    expect(a.url).not.toBe(b.url);
    expect(a.props.release).not.toBe(b.props.release);
  });

  it("keys the same file of two projects behind one shared preview entry apart", () => {
    const host = "pr-1.preview.example.com";
    const a = objectCall(host, key);
    const b = objectCall(host, key.replace("shop", "blog"));
    expect(a.url).not.toBe(b.url);
  });

  it("keys the same file of two apps of one project apart", () => {
    const a = objectCall("shop.example.com", key);
    const b = objectCall("shop.example.com", key.replace("/web/", "/admin/"));
    expect(a.props.app).not.toBe(b.props.app);
    expect(a.url).not.toBe(b.url);
  });

  it("keeps a key's special characters in one path segment each", () => {
    const call = objectCall("h", "prod/shop/web/r1/assets/a b/c?d#e%f.js");
    expect(new URL(call.url).pathname).toBe("/prod/shop/web/r1/assets/a%20b/c%3Fd%23e%25f.js");
    expect(new URL(call.url).search).toBe("");
  });

  it.each([
    ["a key outside any release", "prod/shop"],
    ["an empty segment", "prod/shop//r1/assets/a.js"],
    ["a dot segment", "prod/shop/web/r1/../r2/a.js"],
  ])("refuses %s", (_name, bad) => {
    expect(() => objectCall("h", bad)).toThrow(/outside a release/);
  });
});
