import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  cacheKey,
  freshness,
  serveCached,
  storagePolicy,
  type CacheDeps,
} from "../src/cache";
import { dispatchResult, type RouteDeps } from "../src/index";

// Next 16 emits exactly this for a static route (initialRevalidate: false) —
// s-maxage with no stale-while-revalidate. See next/dist/server/lib/cache-control.
const STATIC_CACHE_CONTROL = "s-maxage=31536000";
// ...and this for a route with both revalidate (60) and expire (300).
const SWR_CACHE_CONTROL = "s-maxage=60, stale-while-revalidate=240";

// Background writes must finish inside the test that started them, or
// vitest-pool-workers' per-test storage isolation tears down underneath them.
let pending: Promise<unknown>[] = [];
const settle = () => Promise.all(pending.splice(0));

beforeEach(() => {
  pending = [];
});
afterEach(settle);

// A clock the tests move by hand: SWR must never depend on wall-clock time.
function fakeClock(start = 1_000_000) {
  let current = start;
  return {
    now: () => current,
    advanceSeconds: (seconds: number) => {
      current += seconds * 1000;
    },
  };
}

// The workerd cache is shared across tests; a fresh key per test keeps entries
// from leaking between them.
let seq = 0;
const uniqueKey = () => `https://cache.ocel/build/entry-${seq++}`;
const uniqueBuild = () => `build-${seq++}`;

function cacheDeps(now: () => number): CacheDeps {
  return {
    cache: caches.default,
    waitUntil: (promise) => {
      pending.push(promise);
    },
    now,
  };
}

function originReturning(body: string, init: ResponseInit = {}) {
  let calls = 0;
  return {
    calls: () => calls,
    origin: async () => {
      calls++;
      return new Response(body, {
        status: 200,
        headers: { "cache-control": STATIC_CACHE_CONTROL },
        ...init,
      });
    },
  };
}

const stored = (key: string) => caches.default.match(new Request(key));

describe("storagePolicy", () => {
  it("reads a static route's s-maxage with no stale window", () => {
    expect(storagePolicy(STATIC_CACHE_CONTROL)).toEqual({
      sMaxAge: 31536000,
      swr: 0,
    });
  });

  it("reads the explicit stale-while-revalidate delta", () => {
    expect(storagePolicy(SWR_CACHE_CONTROL)).toEqual({ sMaxAge: 60, swr: 240 });
  });

  it("refuses to store what Next emits for revalidate: 0", () => {
    expect(
      storagePolicy("private, no-cache, no-store, max-age=0, must-revalidate"),
    ).toBeNull();
  });

  it("refuses to store a response with no cache-control at all", () => {
    expect(storagePolicy(null)).toBeNull();
  });

  it("refuses to store a response with no s-maxage", () => {
    expect(storagePolicy("max-age=600")).toBeNull();
  });

  it("refuses to store a zero-lifetime response", () => {
    expect(storagePolicy("s-maxage=0")).toBeNull();
  });
});

describe("freshness", () => {
  const policy = { sMaxAge: 60, swr: 240 };

  it("is fresh before s-maxage elapses", () => {
    expect(freshness(59, policy)).toBe("fresh");
  });

  it("is stale once s-maxage elapses", () => {
    expect(freshness(60, policy)).toBe("stale");
  });

  it("is stale through the end of the stale window", () => {
    expect(freshness(299, policy)).toBe("stale");
  });

  it("is expired past the stale window", () => {
    expect(freshness(300, policy)).toBe("expired");
  });

  it("expires immediately at s-maxage when there is no stale window", () => {
    expect(freshness(60, { sMaxAge: 60, swr: 0 })).toBe("expired");
  });
});

describe("cacheKey", () => {
  it("scopes the entry to the build and the RSC identity", () => {
    expect(
      cacheKey("abc123", "/index.rsc", new URL("https://app.example/"), []),
    ).toBe("https://cache.ocel/abc123/index.rsc");
  });

  it("separates builds so a redeploy cannot serve the previous build's HTML", () => {
    const url = new URL("https://app.example/");
    expect(cacheKey("build-1", "/", url, [])).not.toBe(
      cacheKey("build-2", "/", url, []),
    );
  });

  it("drops every query param when allowQuery is empty", () => {
    expect(
      cacheKey("b", "/", new URL("https://app.example/?utm=x&a=1"), []),
    ).toBe("https://cache.ocel/b/");
  });

  it("keeps only the allowed params, ordered stably", () => {
    expect(
      cacheKey("b", "/", new URL("https://app.example/?b=2&utm=x&a=1"), [
        "a",
        "b",
      ]),
    ).toBe("https://cache.ocel/b/?a=1&b=2");
  });

  it("keys param order-independently", () => {
    expect(
      cacheKey("b", "/", new URL("https://app.example/?b=2&a=1"), ["a", "b"]),
    ).toBe(
      cacheKey("b", "/", new URL("https://app.example/?a=1&b=2"), ["a", "b"]),
    );
  });

  it("keeps the whole query when the route states no rule", () => {
    expect(
      cacheKey("b", "/", new URL("https://app.example/?a=1&b=2"), undefined),
    ).toBe("https://cache.ocel/b/?a=1&b=2");
  });
});

describe("serveCached", () => {
  let clock: ReturnType<typeof fakeClock>;
  let deps: CacheDeps;
  let key: string;

  beforeEach(() => {
    clock = fakeClock();
    deps = cacheDeps(clock.now);
    key = uniqueKey();
  });

  const get = () => new Request("https://app.example/");

  it("misses, then serves the second request from cache without a second origin fetch", async () => {
    const { origin, calls } = originReturning("page");

    const first = await serveCached(get(), { key }, deps, origin);
    expect(first.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await first.text()).toBe("page");
    await settle();

    const second = await serveCached(get(), { key }, deps, origin);
    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await second.text()).toBe("page");
    expect(calls()).toBe(1);
  });

  it("serves the origin's cache-control, never the rewritten one it is stored under", async () => {
    const { origin } = originReturning("page", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });

    const miss = await serveCached(get(), { key }, deps, origin);
    expect(miss.headers.get("cache-control")).toBe(SWR_CACHE_CONTROL);
    await settle();

    const hit = await serveCached(get(), { key }, deps, origin);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(hit.headers.get("cache-control")).toBe(SWR_CACHE_CONTROL);

    // The internal TTL spanning the stale window stays inside the cache, and the
    // sidecar carrying the real directives never reaches the client.
    expect((await stored(key))?.headers.get("cache-control")).toBe("s-maxage=300");
    expect(hit.headers.get("x-ocel-origin-cache-control")).toBeNull();
    expect(hit.headers.get("x-ocel-stored-at")).toBeNull();
  });

  it("stamps Cache-Tag from the target's tags on put and keeps it out of the served copy", async () => {
    const { origin } = originReturning("page");
    const tags = ["_N_T_/layout", "_N_T_/page", "_N_T_/"];

    const res = await serveCached(get(), { key, tags }, deps, origin);
    await settle();

    expect((await stored(key))?.headers.get("cache-tag")).toBe(tags.join(","));
    expect(res.headers.get("cache-tag")).toBeNull();
  });

  it("omits Cache-Tag when the route has no tags", async () => {
    const { origin } = originReturning("page");

    await serveCached(get(), { key }, deps, origin);
    await settle();

    expect((await stored(key))?.headers.get("cache-tag")).toBeNull();
  });

  it("neither reads nor writes the cache for a draft-mode request", async () => {
    const seed = originReturning("public");
    await serveCached(get(), { key }, deps, seed.origin);
    await settle();

    const draft = originReturning("draft");
    const res = await serveCached(
      new Request("https://app.example/", {
        headers: { cookie: "a=1; __prerender_bypass=token; b=2" },
      }),
      { key },
      deps,
      draft.origin,
    );

    // Read bypassed: the public entry was not served.
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(await res.text()).toBe("draft");
    expect(draft.calls()).toBe(1);

    // Write bypassed: the draft response did not overwrite the public entry.
    await settle();
    expect(await (await stored(key))?.text()).toBe("public");
  });

  it("does not let a cookie merely ending in the draft cookie's name bypass", async () => {
    const { origin } = originReturning("page");
    const res = await serveCached(
      new Request("https://app.example/", {
        headers: { cookie: "not__prerender_bypass=x" },
      }),
      { key },
      deps,
      origin,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("bypasses a non-GET request, which our synthetic GET key would otherwise match", async () => {
    const seed = originReturning("page");
    await serveCached(get(), { key }, deps, seed.origin);
    await settle();

    const post = originReturning("action-result");
    const res = await serveCached(
      new Request("https://app.example/", { method: "POST" }),
      { key },
      deps,
      post.origin,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(await res.text()).toBe("action-result");
    await settle();
    expect(await (await stored(key))?.text()).toBe("page");
  });

  it("does not cache a non-200 response", async () => {
    // _global-error prerenders with initialStatus 500.
    const { origin } = originReturning("error", {
      status: 500,
      headers: { "cache-control": STATIC_CACHE_CONTROL },
    });

    const res = await serveCached(get(), { key }, deps, origin);
    await settle();

    expect(res.status).toBe(500);
    expect(await stored(key)).toBeUndefined();
  });

  it("does not cache a response the origin marked uncacheable", async () => {
    const { origin } = originReturning("private", {
      headers: {
        "cache-control":
          "private, no-cache, no-store, max-age=0, must-revalidate",
      },
    });

    await serveCached(get(), { key }, deps, origin);
    await settle();

    expect(await stored(key)).toBeUndefined();
  });

  it("serves stale and triggers exactly one refresh inside the stale window", async () => {
    const first = originReturning("v1", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    await serveCached(get(), { key }, deps, first.origin);
    await settle();

    clock.advanceSeconds(120); // past s-maxage=60, inside the 240s stale window

    const refresh = originReturning("v2", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    const stale = await serveCached(get(), { key }, deps, refresh.origin);

    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await stale.text()).toBe("v1");
    expect(pending).toHaveLength(1);

    await settle();
    expect(refresh.calls()).toBe(1);
    expect(first.calls()).toBe(1);

    // The refresh replaced the entry, which is fresh again as of the new stamp.
    const next = await serveCached(get(), { key }, deps, refresh.origin);
    expect(next.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await next.text()).toBe("v2");
  });

  it("keeps serving the prior entry when a background refresh throws", async () => {
    const { origin } = originReturning("v1", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    await serveCached(get(), { key }, deps, origin);
    await settle();

    clock.advanceSeconds(120);

    const stale = await serveCached(get(), { key }, deps, async () => {
      throw new Error("origin down");
    });
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await stale.text()).toBe("v1");

    // The rejection is contained, and the entry survives it intact.
    await expect(settle()).resolves.toBeDefined();
    expect(await (await stored(key))?.text()).toBe("v1");

    const after = await serveCached(get(), { key }, deps, origin);
    expect(after.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await after.text()).toBe("v1");
  });

  it("does not poison the entry when a background refresh returns a 5xx", async () => {
    const { origin } = originReturning("v1", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    await serveCached(get(), { key }, deps, origin);
    await settle();

    clock.advanceSeconds(120);

    await serveCached(
      get(),
      { key },
      deps,
      async () => new Response("boom", { status: 502 }),
    );
    await settle();

    expect(await (await stored(key))?.text()).toBe("v1");
  });

  it("re-fetches once the stale window has passed", async () => {
    const { origin } = originReturning("v1", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    await serveCached(get(), { key }, deps, origin);
    await settle();

    clock.advanceSeconds(301); // past s-maxage + swr

    const refresh = originReturning("v2", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    const res = await serveCached(get(), { key }, deps, refresh.origin);

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await res.text()).toBe("v2");
    expect(refresh.calls()).toBe(1);
    await settle();
  });

  it("counts age from our own timestamp, not the cache's Age header", async () => {
    const { origin } = originReturning("page", {
      headers: { "cache-control": SWR_CACHE_CONTROL },
    });
    await serveCached(get(), { key }, deps, origin);
    await settle();

    // Wall-clock time has barely moved, but our clock says the entry is stale.
    clock.advanceSeconds(120);
    const res = await serveCached(get(), { key }, deps, origin);
    expect(res.headers.get("x-ocel-cache")).toBe("STALE");
    await settle();
  });
});

// End to end through dispatchResult, which owns eligibility and key construction.
describe("dispatchResult caching", () => {
  const rscConfig = {
    header: "rsc",
    suffix: ".rsc",
    prefetchSegmentHeader: "next-router-segment-prefetch",
    prefetchSegmentSuffix: ".segment.rsc",
    prefetchSegmentDirSuffix: ".segments",
  };

  const tags = ["_N_T_/layout", "_N_T_/page"];

  function manifest(buildId: string): RouteDeps["manifest"] {
    return {
      buildId,
      basePath: "",
      pathnames: [],
      routes: { rsc: rscConfig },
      dispatch: {
        "/": { kind: "prerender", id: "/", tags, allowQuery: [] },
        "/index.rsc": { kind: "prerender", id: "/", tags, allowQuery: [] },
        "/api/documents": { kind: "lambda", id: "/api/documents" },
      },
    };
  }

  function deps(
    buildId: string,
    body: string,
    now: () => number,
  ): RouteDeps & { calls: () => number } {
    let calls = 0;
    return {
      manifest: manifest(buildId),
      functionUrls: {
        "/": "https://fn.example.com",
        "/api/documents": "https://fn.example.com",
      },
      assets: { fetch: async () => new Response("", { status: 404 }) },
      fetch: (async () => {
        calls++;
        return new Response(body, {
          status: 200,
          headers: { "cache-control": STATIC_CACHE_CONTROL },
        });
      }) as unknown as typeof fetch,
      cache: cacheDeps(now),
      calls: () => calls,
    };
  }

  const home = { resolvedPathname: "/", invocationTarget: { pathname: "/" } };
  const homeRequest = (init?: RequestInit) =>
    new Request("https://app.example/", init);

  it("serves a prerender route's second GET from cache with no second origin fetch", async () => {
    const d = deps(uniqueBuild(), "rendered", fakeClock().now);

    const first = await dispatchResult(home, homeRequest(), d);
    expect(first.headers.get("x-ocel-cache")).toBe("MISS");
    await settle();

    const second = await dispatchResult(home, homeRequest(), d);
    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await second.text()).toBe("rendered");
    expect(d.calls()).toBe(1);
  });

  it("carries the origin's cache-control on the served response", async () => {
    const d = deps(uniqueBuild(), "rendered", fakeClock().now);

    await dispatchResult(home, homeRequest(), d);
    await settle();

    const hit = await dispatchResult(home, homeRequest(), d);
    expect(hit.headers.get("cache-control")).toBe(STATIC_CACHE_CONTROL);
  });

  it("stamps the manifest's tags onto the stored entry", async () => {
    const build = uniqueBuild();
    const d = deps(build, "rendered", fakeClock().now);

    await dispatchResult(home, homeRequest(), d);
    await settle();

    expect((await stored(`https://cache.ocel/${build}/`))?.headers.get("cache-tag")).toBe(
      tags.join(","),
    );
  });

  it("keys an RSC request apart from the document it shares a pathname with", async () => {
    const build = uniqueBuild();
    const d = deps(build, "document", fakeClock().now);

    await dispatchResult(home, homeRequest(), d);
    await settle();

    // Cloudflare ignores Vary, so the RSC variant must not hit the document's
    // entry — it has its own identity, and so its own key.
    const rsc = await dispatchResult(home, homeRequest({ headers: { rsc: "1" } }), d);
    expect(rsc.headers.get("x-ocel-cache")).toBe("MISS");
    await settle();

    expect(await stored(`https://cache.ocel/${build}/index.rsc`)).toBeDefined();
  });

  it("never caches a kind:lambda route", async () => {
    const build = uniqueBuild();
    const d = deps(build, "from-lambda", fakeClock().now);
    const api = {
      resolvedPathname: "/api/documents",
      invocationTarget: { pathname: "/api/documents" },
    };
    const request = () => new Request("https://app.example/api/documents");

    const first = await dispatchResult(api, request(), d);
    await settle();
    const second = await dispatchResult(api, request(), d);

    expect(first.headers.get("x-ocel-cache")).toBeNull();
    expect(second.headers.get("x-ocel-cache")).toBeNull();
    expect(d.calls()).toBe(2);
    expect(await stored(`https://cache.ocel/${build}/api/documents`)).toBeUndefined();
  });

  it("misses after a redeploy, rather than serving the previous build's HTML", async () => {
    const clock = fakeClock();
    const before = deps(uniqueBuild(), "old", clock.now);

    await dispatchResult(home, homeRequest(), before);
    await settle();

    const after = deps(uniqueBuild(), "new", clock.now);
    const res = await dispatchResult(home, homeRequest(), after);

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await res.text()).toBe("new");
    await settle();
  });

  it("does not cache an RSC variant that has no dispatch entry of its own", async () => {
    const build = uniqueBuild();
    const d = deps(build, "document", fakeClock().now);
    d.manifest.dispatch["/plain"] = { kind: "prerender", id: "/", allowQuery: [] };

    // '/plain.rsc' is absent from dispatch, so resolveRsc falls back to an
    // identity '/plain' shares with the document response.
    const res = await dispatchResult(
      { resolvedPathname: "/plain", invocationTarget: { pathname: "/plain" } },
      new Request("https://app.example/plain", { headers: { rsc: "1" } }),
      d,
    );
    await settle();

    expect(res.headers.get("x-ocel-cache")).toBeNull();
    expect(await stored(`https://cache.ocel/${build}/plain`)).toBeUndefined();
  });

  it("leaves routing untouched when no cache is wired in", async () => {
    const { cache: _cache, ...uncached } = deps(
      uniqueBuild(),
      "rendered",
      fakeClock().now,
    );

    const res = await dispatchResult(home, homeRequest(), uncached);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-ocel-cache")).toBeNull();
  });
});
