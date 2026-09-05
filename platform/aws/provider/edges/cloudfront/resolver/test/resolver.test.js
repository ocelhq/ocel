import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import fixture from "@platform/edge-contract/fixtures/cache-key" with { type: "json" };

const source = readFileSync(new URL("../src/resolver.js", import.meta.url), "utf8");

function load(cf) {
  const body = source.replace(/^import cf from 'cloudfront';\n/m, "");
  return new Function("cf", `${body}\nreturn handler;`)(cf);
}

class KeyMissing extends Error {}

class StoreUnreachable extends Error {}

function cloudfront(entries, { failing = false } = {}) {
  const origins = [];
  return {
    origins,
    kvs: () => ({
      get: async (key) => {
        if (failing) throw new StoreUnreachable("InternalServerException");
        if (!Object.hasOwn(entries, key)) throw new KeyMissing(`no key ${key}`);
        return entries[key];
      },
      exists: async (key) => {
        if (failing) throw new StoreUnreachable("InternalServerException");
        return Object.hasOwn(entries, key);
      },
    }),
    updateRequestOrigin: (origin) => origins.push(origin),
  };
}

const RELEASE = fixture.release;

const ASSET_PREFIX = "production/proj1/web/r3f8a1c9d/assets";

const ROUTE = {
  stack: "ocel.proj1.production",
  origin: "abcdef.lambda-url.eu-west-1.on.aws",
  release: RELEASE,
  assets: "ocel-assets.s3.eu-west-1.amazonaws.com",
  assetPrefix: ASSET_PREFIX,
  secret: "5e884898da28047151d0e56f8dc6292773603d0d",
};

function request(uri, headers = {}, cookies = {}) {
  const wire = { host: { value: "shop.example.com" } };
  for (const [name, value] of Object.entries(headers)) wire[name] = { value };
  const jar = {};
  for (const [name, value] of Object.entries(cookies)) jar[name] = { value };
  return { request: { method: "GET", uri, querystring: {}, headers: wire, cookies: jar } };
}

async function resolve(event, entries = { "shop.example.com": ROUTE }, options) {
  const cf = cloudfront(entries, options);
  const answered = await load(cf)(event);
  return { answered, origins: cf.origins };
}

async function keyFor(testCase) {
  const cookies = testCase.draft ? { __prerender_bypass: "1" } : {};
  const { answered } = await resolve(request(testCase.pathname, testCase.headers, cookies));
  return answered.headers["x-ocel-cache-key"].value;
}

describe("the resolver", () => {
  it("answers a hostname no release claims itself, and reaches no origin", async () => {
    const { answered, origins } = await resolve(request("/blog"), {});

    expect(answered.statusCode).toBe(404);
    expect(answered.headers["x-ocel-edge"].value).toBe("cloudfront");
    expect(origins).toHaveLength(0);
  });

  it("answers a request carrying no host header the same way", async () => {
    const event = request("/blog");
    delete event.request.headers.host;

    const { answered, origins } = await resolve(event);

    expect(answered.statusCode).toBe(404);
    expect(origins).toHaveLength(0);
  });

  it("tells a viewer the store is unreadable rather than that the site does not exist", async () => {
    const { answered, origins } = await resolve(request("/blog"), { "shop.example.com": ROUTE }, {
      failing: true,
    });

    expect(answered.statusCode).toBe(503);
    expect(answered.headers["x-ocel-edge"].value).toBe("cloudfront");
    expect(origins).toHaveLength(0);
  });

  it("sends a static asset to the release's prefix in the bucket, signed and without the secret", async () => {
    const { answered, origins } = await resolve(request("/_next/static/chunks/main.js"));

    expect(origins).toEqual([
      {
        domainName: ROUTE.assets,
        originPath: `/${ASSET_PREFIX}`,
        originAccessControlConfig: {
          enabled: true,
          signingBehavior: "always",
          signingProtocol: "sigv4",
          originType: "s3",
        },
        customHeaders: {},
      },
    ]);
    expect(JSON.stringify(origins)).not.toContain(ROUTE.secret);
    expect(answered.headers["x-forwarded-host"].value).toBe("shop.example.com");
  });

  it("names no origin path when the release has no asset prefix", async () => {
    const { origins } = await resolve(request("/_next/static/chunks/main.js"), {
      "shop.example.com": { ...ROUTE, assetPrefix: "" },
    });

    expect(origins[0]).not.toHaveProperty("originPath");
  });

  it("trims a stored prefix that already carries slashes", async () => {
    const { origins } = await resolve(request("/_next/static/chunks/main.js"), {
      "shop.example.com": { ...ROUTE, assetPrefix: `/${ASSET_PREFIX}/` },
    });

    expect(origins[0].originPath).toBe(`/${ASSET_PREFIX}`);
  });

  it("sends everything else to the release's entry function with the secret it demands", async () => {
    const { answered, origins } = await resolve(request("/blog"));

    expect(origins).toHaveLength(1);
    expect(origins[0].domainName).toBe(ROUTE.origin);
    expect(origins[0].customHeaders["x-ocel-origin-secret"]).toBe(ROUTE.secret);
    expect(origins[0].originAccessControlConfig).toEqual({ enabled: false });
    expect(answered.headers["x-forwarded-host"].value).toBe("shop.example.com");
    expect(answered.headers["x-ocel-cache-key"].value).toBe(`${RELEASE}/blog`);
  });

  it("lowercases the host it looks up and forwards", async () => {
    const event = request("/blog");
    event.request.headers.host = { value: "SHOP.Example.com" };

    const { answered } = await resolve(event);

    expect(answered.headers["x-forwarded-host"].value).toBe("shop.example.com");
  });
});

describe("the headers a viewer cannot forge past the resolver", () => {
  it("never lets a viewer's own origin secret reach the origin", async () => {
    const { answered, origins } = await resolve(
      request("/blog", { "x-ocel-origin-secret": "forged" }),
    );

    expect(answered.headers["x-ocel-origin-secret"]).toBeUndefined();
    expect(origins[0].customHeaders["x-ocel-origin-secret"]).toBe(ROUTE.secret);
  });

  it("drops every other control header the origin would otherwise trust", async () => {
    const { answered } = await resolve(
      request("/blog", {
        "x-ocel-entry": "1",
        "next-resume": "1",
        "x-ocel-origin-secret": "forged",
        "x-middleware-skip": "1",
      }),
    );

    for (const name of [
      "x-ocel-entry",
      "next-resume",
      "x-ocel-origin-secret",
      "x-middleware-skip",
    ]) {
      expect(answered.headers[name]).toBeUndefined();
    }
  });

  it("carries a viewer's own x-ocel- header the origin has no meaning for through untouched", async () => {
    const { answered } = await resolve(request("/blog", { "x-ocel-probe": "probe-value" }));

    expect(answered.headers["x-ocel-probe"].value).toBe("probe-value");
  });

  it("keys on the host and variant it computed, not on the one a viewer sent", async () => {
    const { answered } = await resolve(
      request("/blog", { "x-ocel-cache-key": "forged", "x-forwarded-host": "evil.example.com" }),
    );

    expect(answered.headers["x-ocel-cache-key"].value).toBe(`${RELEASE}/blog`);
    expect(answered.headers["x-forwarded-host"].value).toBe("shop.example.com");
  });
});

describe("the cache key the resolver computes", () => {
  for (const testCase of fixture.cases.filter((c) => c.variantPrerendered !== null)) {
    it(testCase.name, async () => {
      const draft = testCase.draft ? ".draft" : "";

      expect(await keyFor(testCase)).toBe(`${RELEASE}${testCase.variantPrerendered}${draft}`);
    });
  }
});

describe("the resolver reads no routing manifest, so it keys every route as if it were static", () => {
  const diverging = fixture.cases.filter(
    (c) => c.variantPrerendered !== c.variantPartiallyStatic && c.variantPrerendered !== null,
  );

  it("has cases the two rendering modes disagree about", () => {
    expect(diverging.length).toBeGreaterThan(0);
  });

  for (const testCase of diverging) {
    it(`${testCase.name}, which a partially-static route would not cache at all`, async () => {
      const draft = testCase.draft ? ".draft" : "";

      expect(testCase.variantPartiallyStatic).toBeNull();
      expect(await keyFor(testCase)).toBe(`${RELEASE}${testCase.variantPrerendered}${draft}`);
    });
  }
});

describe("the keys the resolver gives requests the shared fixture records no cacheable variant for", () => {
  const uncacheable = fixture.cases.filter((c) => c.variantPrerendered === null);

  it("gives every one of them a key of its own", async () => {
    const keys = await Promise.all(uncacheable.map(keyFor));

    expect(new Set(keys).size).toBe(uncacheable.length);
  });

  it("keeps them apart from the request that is cacheable on the same pathname", async () => {
    const cacheable = await keyFor({ pathname: "/blog", headers: { rsc: "1" }, draft: false });
    const keys = await Promise.all(uncacheable.map(keyFor));

    expect(keys).not.toContain(cacheable);
  });

  it("keeps a prefetch of 0 and a prefetch of 2 on the same route apart", async () => {
    const zero = await keyFor({
      pathname: "/blog",
      headers: { rsc: "1", "next-router-prefetch": "0" },
      draft: false,
    });
    const two = await keyFor({
      pathname: "/blog",
      headers: { rsc: "1", "next-router-prefetch": "2" },
      draft: false,
    });

    expect(zero).not.toBe(two);
  });

  it("keeps two interception bases on the same route apart", async () => {
    const one = await keyFor({
      pathname: "/photo",
      headers: { rsc: "1", "next-url": "/photo/1", "next-router-prefetch": "0" },
      draft: false,
    });
    const other = await keyFor({
      pathname: "/photo",
      headers: { rsc: "1", "next-url": "/feed", "next-router-prefetch": "0" },
      draft: false,
    });

    expect(one).not.toBe(other);
  });
});
