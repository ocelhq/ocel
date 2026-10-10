import { describe, expect, it } from "vitest";
import { buildObjectCall, buildOriginCall, buildServeRequest } from "../src/serve-key";

const key = "prod/shop/web/r1a2b3c4d/assets/_next/static/app.js";

describe("the cache key of an object read through Serve", () => {
  it("carries the host, app and release of the key it reads", () => {
    const call = buildObjectCall("shop.example.com", key);
    expect(call.props).toEqual({
      kind: "object",
      host: "shop.example.com",
      app: "web",
      release: "r1a2b3c4d",
    });
    expect(new URL(call.url).pathname).toBe(`/${key}`);
  });

  it("keys the same path under another host apart", () => {
    const a = buildObjectCall("a.example.com", key);
    const b = buildObjectCall("b.example.com", key);
    expect(a.url).toBe(b.url);
    expect(a.props).not.toEqual(b.props);
  });

  it("keys the same file of another release apart", () => {
    const next = key.replace("r1a2b3c4d", "r5e6f7a8b");
    const a = buildObjectCall("shop.example.com", key);
    const b = buildObjectCall("shop.example.com", next);
    expect(a.url).not.toBe(b.url);
    expect(a.props.release).not.toBe(b.props.release);
  });

  it("keys the same file of two projects behind one shared preview entry apart", () => {
    const host = "pr-1.preview.example.com";
    const a = buildObjectCall(host, key);
    const b = buildObjectCall(host, key.replace("shop", "blog"));
    expect(a.url).not.toBe(b.url);
  });

  it("keys the same file of two apps of one project apart", () => {
    const a = buildObjectCall("shop.example.com", key);
    const b = buildObjectCall("shop.example.com", key.replace("/web/", "/admin/"));
    expect(a.props.app).not.toBe(b.props.app);
    expect(a.url).not.toBe(b.url);
  });

  it("keeps a key's special characters in one path segment each", () => {
    const call = buildObjectCall("h", "prod/shop/web/r1/assets/a b/c?d#e%f.js");
    expect(new URL(call.url).pathname).toBe("/prod/shop/web/r1/assets/a%20b/c%3Fd%23e%25f.js");
    expect(new URL(call.url).search).toBe("");
  });

  it.each([
    ["a key outside any release", "prod/shop"],
    ["an empty segment", "prod/shop//r1/assets/a.js"],
    ["a dot segment", "prod/shop/web/r1/../r2/a.js"],
  ])("refuses %s", (_name, bad) => {
    expect(() => buildObjectCall("h", bad)).toThrow(/outside a release/);
  });
});

describe("the cache key of a page read through Serve from the origin", () => {
  const coordinates = { host: "shop.example.com", app: "web", release: "r1a2b3c4d" };
  const origin = "https://abc123.lambda-url.eu-west-2.on.aws/blog/a?page=2";

  it("is the origin URL Serve fetches, with the host, app, release and variant", () => {
    const call = buildOriginCall(origin, { ...coordinates, variant: "rsc" });
    expect(call).toEqual({
      url: origin,
      props: { ...coordinates, kind: "origin", variant: "rsc" },
    });
  });

  it("keys two variants of one page apart", () => {
    const html = buildOriginCall(origin, coordinates);
    const rsc = buildOriginCall(origin, { ...coordinates, variant: "rsc" });
    expect(html.url).toBe(rsc.url);
    expect(html.props).not.toEqual(rsc.props);
  });
});

describe("the request the gateway hands Serve", () => {
  it("carries none of the visitor's cookies or credentials", () => {
    const visitor = new Request("https://shop.example.com/blog/a", {
      headers: {
        cookie: "session=1",
        authorization: "Bearer t",
        "if-none-match": '"e1"',
        rsc: "1",
      },
    });

    const request = buildServeRequest("https://abc123.lambda-url.eu-west-2.on.aws/blog/a", visitor);

    expect(request.headers.get("cookie")).toBeNull();
    expect(request.headers.get("authorization")).toBeNull();
    expect(request.headers.get("if-none-match")).toBe('"e1"');
    expect(request.headers.get("rsc")).toBe("1");
    expect(request.url).toBe("https://abc123.lambda-url.eu-west-2.on.aws/blog/a");
  });

  it("keeps the visitor's method", () => {
    const visitor = new Request("https://shop.example.com/", { method: "HEAD" });
    expect(buildServeRequest("https://serve.example/", visitor).method).toBe("HEAD");
  });

  it("is a plain GET when no visitor asked", () => {
    expect(buildServeRequest("https://serve.example/").method).toBe("GET");
  });
});
