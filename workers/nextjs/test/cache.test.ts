import { describe, expect, it } from "vitest";

import {
  cacheKey,
  freshness,
  serveCached,
  storagePolicy,
  type CacheDeps,
  type CacheTarget,
} from "../src/cache";

// A CacheDeps backed by the real workerd Cache, with a manual clock and a
// waitUntil that records background work so tests can flush it deterministically.
function testDeps(clock: { ms: number }): CacheDeps & {
  flush: () => Promise<void>;
} {
  const pending: Promise<unknown>[] = [];
  return {
    cache: caches.default,
    now: () => clock.ms,
    waitUntil: (promise) => {
      pending.push(promise);
    },
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
}

// An origin returning a fixed response and counting how often it was invoked.
type CountingOrigin = (() => Promise<Response>) & { calls: number };

function countingOrigin(
  cacheControl: string,
  body = "rendered",
  status = 200,
): CountingOrigin {
  const fn = (async () => {
    fn.calls++;
    return new Response(body, {
      status,
      headers: { "cache-control": cacheControl },
    });
  }) as CountingOrigin;
  fn.calls = 0;
  return fn;
}

const req = (url = "https://app.example/", init?: RequestInit) =>
  new Request(url, init);

describe("storagePolicy", () => {
  it("reads a bare s-maxage as a zero-swr policy", () => {
    expect(storagePolicy("s-maxage=31536000")).toEqual({
      sMaxAge: 31536000,
      swr: 0,
    });
  });

  it("reads s-maxage plus stale-while-revalidate", () => {
    expect(storagePolicy("s-maxage=60, stale-while-revalidate=30")).toEqual({
      sMaxAge: 60,
      swr: 30,
    });
  });

  it("refuses to store private / no-store / no-cache responses", () => {
    expect(
      storagePolicy("private, no-cache, no-store, max-age=0, must-revalidate"),
    ).toBeNull();
  });

  it("refuses responses with no positive s-maxage", () => {
    expect(storagePolicy("max-age=0")).toBeNull();
    expect(storagePolicy("s-maxage=0")).toBeNull();
    expect(storagePolicy(null)).toBeNull();
  });
});

describe("freshness", () => {
  const policy = { sMaxAge: 60, swr: 30 };

  it("is fresh before s-maxage", () => {
    expect(freshness(10, policy)).toBe("fresh");
  });

  it("is stale within the swr window", () => {
    expect(freshness(70, policy)).toBe("stale");
  });

  it("is expired past s-maxage + swr", () => {
    expect(freshness(100, policy)).toBe("expired");
  });

  it("treats a zero-swr policy as fresh-then-expired", () => {
    expect(freshness(59, { sMaxAge: 60, swr: 0 })).toBe("fresh");
    expect(freshness(60, { sMaxAge: 60, swr: 0 })).toBe("expired");
  });
});

describe("cacheKey", () => {
  it("scopes the key by buildId so a redeploy misses", () => {
    const url = new URL("https://app.example/blog");
    expect(cacheKey("build-a", "/blog", url, [])).not.toBe(
      cacheKey("build-b", "/blog", url, []),
    );
  });

  it("drops query params the route does not allow", () => {
    const url = new URL("https://app.example/blog?page=2&ref=x");
    expect(cacheKey("b", "/blog", url, [])).toBe("https://cache.ocel/b/blog");
  });

  it("keeps allowed query params, normalized by name", () => {
    const url = new URL("https://app.example/blog?b=2&a=1");
    expect(cacheKey("b", "/blog", url, ["a", "b"])).toBe(
      "https://cache.ocel/b/blog?a=1&b=2",
    );
  });
});

describe("serveCached", () => {
  // Distinct keys per test keep entries from bleeding across cases even if the
  // isolate's Cache is reused.
  const target = (name: string, tags?: string[]): CacheTarget => ({
    key: `https://cache.ocel/build/${name}`,
    tags,
  });

  it("misses, stores, then serves the second GET from cache without a re-fetch", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60");

    const first = await serveCached(
      req(),
      target("hit"),
      deps,
      origin,
      origin,
    );
    expect(first.headers.get("x-ocel-cache")).toBe("MISS");
    await deps.flush();

    clock.ms = 1_000;
    const second = await serveCached(
      req(),
      target("hit"),
      deps,
      origin,
      origin,
    );

    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
    expect(origin.calls).toBe(1);
  });

  it("misses without mutating an immutable origin response", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    // A real fetch() response has immutable headers; Response.redirect is the
    // one constructor that reproduces that guard in the test runtime.
    const origin = async () => Response.redirect("https://app.example/next", 302);

    const first = await serveCached(req(), target("immutable"), deps, origin, origin);

    expect(first.headers.get("x-ocel-cache")).toBe("MISS");
    expect(first.headers.get("cache-control")).toBe(
      "public, max-age=0, must-revalidate",
    );
  });

  it("serves the browser a revalidating Cache-Control, never the stored TTL", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60");

    const miss = await serveCached(req(), target("cc"), deps, origin, origin);
    await deps.flush();
    clock.ms = 1_000;
    const hit = await serveCached(req(), target("cc"), deps, origin, origin);

    for (const res of [miss, hit]) {
      expect(res.headers.get("cache-control")).toBe(
        "public, max-age=0, must-revalidate",
      );
      expect(res.headers.get("x-ocel-origin-cache-control")).toBeNull();
    }
  });

  it("bypasses read and write when the draft cookie is present", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60");

    const drafted = await serveCached(
      req("https://app.example/", {
        headers: { cookie: "__prerender_bypass=1" },
      }),
      target("draft"),
      deps,
      origin,
      origin,
    );
    expect(drafted.headers.get("x-ocel-cache")).toBe("BYPASS");
    await deps.flush();

    // Nothing was written, so a subsequent public GET is still a MISS.
    const after = await serveCached(
      req(),
      target("draft"),
      deps,
      origin,
      origin,
    );
    expect(after.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("bypasses non-GET methods", async () => {
    const deps = testDeps({ ms: 0 });
    const origin = countingOrigin("s-maxage=60");

    const res = await serveCached(
      req("https://app.example/", { method: "POST" }),
      target("post"),
      deps,
      origin,
      origin,
    );
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("misses after a redeploy changes the cache key", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60");

    await serveCached(req(), target("old-build"), deps, origin, origin);
    await deps.flush();

    clock.ms = 1_000;
    const redeploy = await serveCached(
      req(),
      target("new-build"),
      deps,
      origin,
      origin,
    );
    expect(redeploy.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });

  it("serves stale within the swr window and refreshes exactly once", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1, stale-while-revalidate=100");
    const refresh = countingOrigin("s-maxage=1, stale-while-revalidate=100");

    await serveCached(req(), target("swr"), deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000; // 5s old: past s-maxage=1, inside the 100s stale window
    const stale = await serveCached(req(), target("swr"), deps, origin, refresh);

    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  it("keeps the prior entry servable when a background refresh throws", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1, stale-while-revalidate=100");
    const failing = async () => {
      throw new Error("origin down");
    };

    await serveCached(req(), target("poison"), deps, origin, failing);
    await deps.flush();

    clock.ms = 5_000;
    const stale = await serveCached(
      req(),
      target("poison"),
      deps,
      origin,
      failing,
    );
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    await deps.flush(); // must not reject

    const again = await serveCached(
      req(),
      target("poison"),
      deps,
      origin,
      failing,
    );
    expect(again.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await again.text()).toBe("rendered");
  });

  it("does not store a non-200 origin response", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60", "unavailable", 503);

    const first = await serveCached(req(), target("err"), deps, origin, origin);
    expect(first.headers.get("x-ocel-cache")).toBe("MISS");
    await deps.flush();

    clock.ms = 1_000;
    const second = await serveCached(req(), target("err"), deps, origin, origin);
    expect(second.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });

  it("stamps the manifest tags onto the stored entry", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60");
    const t = target("tagged", ["a", "b"]);

    await serveCached(req(), t, deps, origin, origin);
    await deps.flush();

    const stored = await caches.default.match(new Request(t.key));
    expect(stored?.headers.get("cache-tag")).toBe("a,b");
  });
});
