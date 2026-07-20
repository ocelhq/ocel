import { describe, expect, it } from "vitest";

import {
  cacheKey,
  evaluate,
  freshness,
  serveCached,
  storagePolicy,
  storeInColo,
  variantPath,
  type CacheDeps,
  type CacheTarget,
  type EntryMeta,
} from "../src/cache";
import type { TagVerdict } from "../src/tag-clock";

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

describe("evaluate", () => {
  const at = (lastModified: number, over: Partial<EntryMeta> = {}): EntryMeta => ({
    lastModified,
    ...over,
  });

  it("is fresh before revalidate with no tag staleness", () => {
    expect(evaluate(at(0, { revalidate: 60, expiration: 600 }), 30_000, false)).toBe("fresh");
  });

  it("is stale after revalidate but before expiration", () => {
    expect(evaluate(at(0, { revalidate: 60, expiration: 600 }), 120_000, false)).toBe("stale");
  });

  it("is expired at or past expiration once stale", () => {
    expect(evaluate(at(0, { revalidate: 60, expiration: 600 }), 600_000, false)).toBe("expired");
  });

  it("treats a tag-stale entry as stale even when time-fresh", () => {
    expect(evaluate(at(0, { revalidate: 60, expiration: 600 }), 10_000, true)).toBe("stale");
  });

  it("expires a tag-stale entry that is also past expiration", () => {
    expect(evaluate(at(0, { revalidate: 60, expiration: 600 }), 700_000, true)).toBe("expired");
  });

  it("keeps a static entry (no revalidate) fresh until a tag invalidates it", () => {
    expect(evaluate(at(0, {}), 31_000_000_000, false)).toBe("fresh");
    expect(evaluate(at(0, {}), 10_000, true)).toBe("stale");
  });

  it("never expires a static, tag-stale entry with no expiration window", () => {
    expect(evaluate(at(0, {}), 31_000_000_000, true)).toBe("stale");
  });
});

describe("variantPath", () => {
  const H = (init?: Record<string, string>) => new Headers(init);

  it("maps a plain (non-RSC) request to the document pathname", () => {
    expect(variantPath("/blog", H(), "STATIC")).toBe("/blog");
  });

  it("maps a segment prefetch to an encoded .segment.rsc path", () => {
    const h = H({
      RSC: "1",
      "next-router-prefetch": "1",
      "next-router-segment-prefetch": "/blog/__PAGE__",
    });
    expect(variantPath("/blog", h, "PARTIALLY_STATIC")).toBe(
      "/blog.segments/%2Fblog%2F__PAGE__.segment.rsc",
    );
  });

  it("maps a full-route prefetch (prefetch: 1, no segment) to .prefetch.rsc", () => {
    const h = H({ RSC: "1", "next-router-prefetch": "1" });
    expect(variantPath("/blog", h, "PARTIALLY_STATIC")).toBe("/blog.prefetch.rsc");
  });

  it("maps bare RSC on a STATIC route to .rsc", () => {
    expect(variantPath("/blog", H({ RSC: "1" }), "STATIC")).toBe("/blog.rsc");
  });

  it("is non-cacheable for bare RSC on a PPR route (dynamic navigation)", () => {
    expect(variantPath("/blog", H({ RSC: "1" }), "PARTIALLY_STATIC")).toBeNull();
  });

  it("is non-cacheable for a runtime prefetch (prefetch: 2)", () => {
    const h = H({ RSC: "1", "next-router-prefetch": "2" });
    expect(variantPath("/blog", h, "STATIC")).toBeNull();
  });
});

describe("cacheKey", () => {
  const H = (init?: Record<string, string>) => new Headers(init);

  it("scopes the key by buildId so a redeploy misses", () => {
    const url = new URL("https://app.example/blog");
    const a = cacheKey("build-a", "/blog", url, H(), "STATIC", []);
    const b = cacheKey("build-b", "/blog", url, H(), "STATIC", []);
    expect(a).toEqual({ cacheable: true, key: "https://cache.ocel/build-a/blog" });
    expect(a).not.toEqual(b);
  });

  it("drops query params the route does not allow", () => {
    const url = new URL("https://app.example/blog?page=2&ref=x");
    expect(cacheKey("b", "/blog", url, H(), "STATIC", [])).toEqual({
      cacheable: true,
      key: "https://cache.ocel/b/blog",
    });
  });

  it("keeps allowed query params, normalized by name", () => {
    const url = new URL("https://app.example/blog?b=2&a=1");
    expect(cacheKey("b", "/blog", url, H(), "STATIC", ["a", "b"])).toEqual({
      cacheable: true,
      key: "https://cache.ocel/b/blog?a=1&b=2",
    });
  });

  it("strips _rsc from the key even when the route allows all query", () => {
    const url = new URL("https://app.example/blog?_rsc=abc123");
    // allowQuery undefined => default to all params, but _rsc is always dropped.
    expect(cacheKey("b", "/blog", url, H(), "STATIC", undefined)).toEqual({
      cacheable: true,
      key: "https://cache.ocel/b/blog",
    });
  });

  it("gives an RSC request a different key than the HTML request", () => {
    const url = new URL("https://app.example/blog");
    const html = cacheKey("b", "/blog", url, H(), "STATIC", []);
    const rsc = cacheKey("b", "/blog", url, H({ RSC: "1" }), "STATIC", []);
    expect(html).not.toEqual(rsc);
  });

  it("reports a per-visitor dynamic variant as non-cacheable", () => {
    const url = new URL("https://app.example/blog");
    expect(
      cacheKey("b", "/blog", url, H({ RSC: "1" }), "PARTIALLY_STATIC", []),
    ).toEqual({ cacheable: false });
  });
});

describe("serveCached", () => {
  // Distinct keys per test keep entries from bleeding across cases even if the
  // isolate's Cache is reused.
  const target = (
    name: string,
    over: Partial<CacheTarget> = {},
  ): CacheTarget => ({
    key: `https://cache.ocel/build/${name}`,
    ...over,
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

  it("preserves a PRERENDER status the origin already set, storing it for the next colo HIT", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    // The R2 store tier answers by returning a response already stamped
    // PRERENDER (as dispatch's cachingOrigin does); serveCached must report that
    // tier rather than overwriting it with MISS.
    const origin = (async () =>
      new Response("prerendered", {
        headers: { "cache-control": "s-maxage=60", "x-ocel-cache": "PRERENDER" },
      })) as CountingOrigin;
    origin.calls = 0;

    const first = await serveCached(req(), target("edge"), deps, origin, origin);
    expect(first.headers.get("x-ocel-cache")).toBe("PRERENDER");
    await deps.flush();

    // The prerendered response was memoized into the colo cache, so the next GET
    // is a plain colo HIT.
    clock.ms = 1_000;
    const second = await serveCached(req(), target("edge"), deps, origin, origin);
    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
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
      expect(res.headers.get("x-ocel-entry-modified")).toBeNull();
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

  it("serves stale and refreshes once when the entry is past its revalidate window", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");
    const t = target("swr", { revalidate: 1, expiration: 100 });

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000; // 5s old: past revalidate=1, inside expiration=100
    const stale = await serveCached(req(), t, deps, origin, refresh);
    expect(stale.headers.get("x-ocel-cache")).toBe("HIT");
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  it("falls through to origin (re-consults R2) once past expiration", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1");
    const t = target("exp", { revalidate: 1, expiration: 10 });

    await serveCached(req(), t, deps, origin, origin);
    await deps.flush();

    clock.ms = 20_000; // past expiration=10 => not servable stale, re-fetch.
    const res = await serveCached(req(), t, deps, origin, origin);
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });

  it("serves a time-fresh but tag-invalidated hit stale and refreshes", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");
    const t = target("tag-stale", { revalidate: 3600, expiration: 7200, tags: ["posts"] });
    const clockTags = { async expired() { return true as const; } };

    await serveCached(req(), t, deps, origin, refresh, clockTags);
    await deps.flush();

    clock.ms = 1_000; // time-fresh (age 1s << revalidate 3600) but tag says stale
    const hit = await serveCached(req(), t, deps, origin, refresh, clockTags);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  it("serves stale (not fall-through) when the tag snapshot is untrusted on a hit", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");
    const t = target("untrusted", { revalidate: 3600, expiration: 7200, tags: ["posts"] });
    const clockUntrusted = { async expired() { return "untrusted" as const; } };

    await serveCached(req(), t, deps, origin, refresh, clockUntrusted);
    await deps.flush();

    clock.ms = 1_000;
    const hit = await serveCached(req(), t, deps, origin, refresh, clockUntrusted);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT"); // served, not a miss
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  it("collapses a burst of concurrent misses to a single colo write", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const store = countingOrigin("s-maxage=60");
    const t = target("populate", { revalidate: 60, expiration: 600 });

    // Three concurrent misses on the same cold key.
    await Promise.all([
      serveCached(req(), t, deps, store, store),
      serveCached(req(), t, deps, store, store),
      serveCached(req(), t, deps, store, store),
    ]);
    await deps.flush();

    // Each miss called origin to serve its own request, but only one populate ran.
    const stored = await caches.default.match(new Request(t.key));
    expect(stored).not.toBeUndefined();
    // A second-generation read is a HIT: the single write is intact.
    clock.ms = 1_000;
    const hit = await serveCached(req(), t, deps, store, store);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
  });

  it("storeInColo overwrites the entry with a fresh body and a new modified time", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const t = target("overwrite", { revalidate: 1, expiration: 100 });

    const origin = countingOrigin("s-maxage=1", "old");
    await serveCached(req(), t, deps, origin, origin);
    await deps.flush();

    clock.ms = 10_000;
    await storeInColo(t, deps, new Response("new", { headers: { "cache-control": "s-maxage=1" } }));

    const stored = await caches.default.match(new Request(t.key));
    expect(await stored!.text()).toBe("new");
    expect(stored!.headers.get("x-ocel-entry-modified")).toBe("10000");
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
    const t = target("tagged", { tags: ["a", "b"] });

    await serveCached(req(), t, deps, origin, origin);
    await deps.flush();

    const stored = await caches.default.match(new Request(t.key));
    expect(stored?.headers.get("cache-tag")).toBe("a,b");
  });
});
