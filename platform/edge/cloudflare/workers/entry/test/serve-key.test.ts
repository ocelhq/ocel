import cacheKeyFixture from "@platform/edge-contract/fixtures/cache-key" with { type: "json" };
import { describe, expect, it } from "vitest";
import {
  buildCanonicalUrl,
  buildObjectCall,
  buildOriginCall,
  buildServeRequest,
  buildVariantPath,
} from "../src/serve-key";
import { hasDraftCookie } from "../src/serve-prerender";

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

describe("the shared cache-key variants", () => {
  const H = (init?: Record<string, string>) => new Headers(init);

  for (const testCase of cacheKeyFixture.cases) {
    it(testCase.name, () => {
      for (const renderingMode of ["STATIC", undefined] as const) {
        expect(buildVariantPath(testCase.pathname, H(testCase.headers), renderingMode)).toBe(
          testCase.variantPrerendered,
        );
      }
      expect(buildVariantPath(testCase.pathname, H(testCase.headers), "PARTIALLY_STATIC")).toBe(
        testCase.variantPartiallyStatic,
      );

      const cookie = testCase.draft ? "__prerender_bypass=1" : "other=1";
      expect(hasDraftCookie(new Request("https://app.example/", { headers: { cookie } }))).toBe(
        testCase.draft,
      );
    });
  }
});

describe("the path a request's cache variant is stored under", () => {
  const H = (init?: Record<string, string>) => new Headers(init);

  it("maps a plain (non-RSC) request to the document pathname", () => {
    expect(buildVariantPath("/blog", H(), "STATIC")).toBe("/blog");
  });

  it("maps a segment prefetch to an encoded .segment.rsc path", () => {
    const h = H({
      RSC: "1",
      "next-router-prefetch": "1",
      "next-router-segment-prefetch": "/blog/__PAGE__",
    });
    expect(buildVariantPath("/blog", h, "PARTIALLY_STATIC")).toBe(
      "/blog.segments/%2Fblog%2F__PAGE__.segment.rsc",
    );
  });

  it("maps a full-route prefetch (prefetch: 1, no segment) to .prefetch.rsc", () => {
    const h = H({ RSC: "1", "next-router-prefetch": "1" });
    expect(buildVariantPath("/blog", h, "PARTIALLY_STATIC")).toBe("/blog.prefetch.rsc");
  });

  it("maps bare RSC on a STATIC route to .rsc", () => {
    expect(buildVariantPath("/blog", H({ RSC: "1" }), "STATIC")).toBe("/blog.rsc");
  });

  it("maps bare RSC to .rsc when the build declared no rendering mode", () => {
    expect(buildVariantPath("/blog", H({ RSC: "1" }), undefined)).toBe("/blog.rsc");
  });

  it("is non-cacheable for bare RSC on a PPR route (dynamic navigation)", () => {
    expect(buildVariantPath("/blog", H({ RSC: "1" }), "PARTIALLY_STATIC")).toBeNull();
  });

  it("is non-cacheable for a runtime prefetch (prefetch: 2)", () => {
    const h = H({ RSC: "1", "next-router-prefetch": "2" });
    expect(buildVariantPath("/blog", h, "STATIC")).toBeNull();
  });

  it("folds next-url into a segment prefetch's path so an intercepted and a plain navigation to the same URL cannot collide", () => {
    const plain = buildVariantPath(
      "/photo",
      H({ RSC: "1", "next-router-segment-prefetch": "/children" }),
      "PARTIALLY_STATIC",
    );
    const intercepted = buildVariantPath(
      "/photo",
      H({
        RSC: "1",
        "next-router-segment-prefetch": "/children",
        "next-url": "/feed",
      }),
      "PARTIALLY_STATIC",
    );

    expect(plain).not.toBeNull();
    expect(intercepted).not.toBeNull();
    expect(plain).not.toBe(intercepted);
  });

  it("folds next-url into a full-route prefetch and a bare-RSC static path too", () => {
    const withUrl = H({ RSC: "1", "next-router-prefetch": "1", "next-url": "/feed" });
    const withoutUrl = H({ RSC: "1", "next-router-prefetch": "1" });
    expect(buildVariantPath("/photo", withUrl, "PARTIALLY_STATIC")).not.toBe(
      buildVariantPath("/photo", withoutUrl, "PARTIALLY_STATIC"),
    );

    const staticWithUrl = H({ RSC: "1", "next-url": "/feed" });
    const staticWithoutUrl = H({ RSC: "1" });
    expect(buildVariantPath("/photo", staticWithUrl, "STATIC")).not.toBe(
      buildVariantPath("/photo", staticWithoutUrl, "STATIC"),
    );
  });
});

describe("the canonical URL of an origin call", () => {
  const url = (search: string) => new URL(`https://fn.example.com/blog${search}`);

  it("drops query params the route does not allow", () => {
    expect(buildCanonicalUrl(url("?page=2&ref=x"), [])).toBe("https://fn.example.com/blog");
  });

  it("keeps allowed query params, ordered by name", () => {
    expect(buildCanonicalUrl(url("?b=2&a=1"), ["a", "b"])).toBe(
      "https://fn.example.com/blog?a=1&b=2",
    );
  });

  it("keeps every value of an allowed param in the order it came", () => {
    expect(buildCanonicalUrl(url("?a=2&a=1"), ["a"])).toBe("https://fn.example.com/blog?a=2&a=1");
  });

  it("keeps all params when the route allows all query", () => {
    expect(buildCanonicalUrl(url("?b=2&a=1"), undefined)).toBe(
      "https://fn.example.com/blog?a=1&b=2",
    );
  });

  it("strips _rsc even when the route allows all query", () => {
    expect(buildCanonicalUrl(url("?_rsc=abc123"), undefined)).toBe("https://fn.example.com/blog");
  });

  it("names a param twice in the allow list once", () => {
    expect(buildCanonicalUrl(url("?a=1"), ["a", "a"])).toBe("https://fn.example.com/blog?a=1");
  });
});
