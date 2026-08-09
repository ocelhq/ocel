import { describe, expect, it, vi } from "vitest";

import {
  admissionDrawMs,
  admissionJitterMs,
  admitRefresh,
  cacheKey,
  deltaSeconds,
  evaluate,
  refreshBackoffSeconds,
  refreshSentinelTtlSeconds,
  serveCached,
  sentinelUrl,
  serveCachedImage,
  storagePolicy,
  storeInColo,
  variantPath,
  withStatus,
  withVercelCacheAlias,
  type CacheDeps,
  type CacheTarget,
  type EntryMeta,
  type RefreshOutcome,
} from "../src/cache";
import type { TagVerdict } from "../src/tag-clock";
import { coloDeps } from "./cache-deps";

// A CacheDeps backed by the real workerd Cache, with a manual clock and a
// waitUntil that records background work so tests can flush it deterministically.
// `cache` defaults to caches.default but can be swapped for a wrapper (e.g.
// countingCache) that still delegates to the real cache underneath.
function testDeps(
  clock: { ms: number },
  cache: Cache = caches.default,
  // The tier-below double, bound here for the same reason admissionDelay is: one
  // place a seam of CacheDeps is wired, so a test cannot quietly grow its own.
  satisfiedFromBelow?: CacheDeps["satisfiedFromBelow"],
): CacheDeps & {
  flush: () => Promise<void>;
} {
  const pending: Promise<unknown>[] = [];
  return {
    // coloDeps zeroes the admission wait, so every test below reads as it did
    // before that wait existed. The wait's own behaviour — that it happens at
    // all, that it precedes the claim, that its draw is bounded both by the
    // jitter and by the entry's remaining stale window — is asserted where the
    // default is left in place or the seam is driven deliberately.
    ...coloDeps({
      cache,
      satisfiedFromBelow,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
    }),
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
}

// Delegates every call to the real workerd cache, only counting `put`s, so a
// test can assert write cardinality without reimplementing the Cache API.
function countingCache(): Cache & { puts: number } {
  const real = caches.default;
  const counting = {
    puts: 0,
    match: (...args: Parameters<Cache["match"]>) => real.match(...args),
    put: (...args: Parameters<Cache["put"]>) => {
      counting.puts++;
      return real.put(...args);
    },
    delete: (...args: Parameters<Cache["delete"]>) => real.delete(...args),
  };
  return counting as unknown as Cache & { puts: number };
}

// Delegates to the real workerd cache while recording every url it was asked
// about, so a test can assert which synthetic keys a path did and did not touch.
function recordingCache(): Cache & { urls: string[] } {
  const real = caches.default;
  const recording = {
    urls: [] as string[],
    match: (request: RequestInfo, ...rest: unknown[]) => {
      recording.urls.push(new Request(request).url);
      return real.match(request as Request, ...(rest as []));
    },
    put: (request: RequestInfo, response: Response) => {
      recording.urls.push(new Request(request).url);
      return real.put(request as Request, response);
    },
    delete: (request: RequestInfo, ...rest: unknown[]) => {
      recording.urls.push(new Request(request).url);
      return real.delete(request as Request, ...(rest as []));
    },
  };
  return recording as unknown as Cache & { urls: string[] };
}

// A Cache double modelling only what the sentinel needs: retention against the
// test's own clock. The real workerd cache expires on real time, which no
// injected clock can advance.
function ttlCache(clock: { ms: number }): Cache {
  const stored = new Map<string, number>();
  return {
    match: async (request: Request) => {
      const until = stored.get(request.url);
      if (until === undefined || clock.ms >= until) return undefined;
      return new Response(null);
    },
    put: async (request: Request, response: Response) => {
      const ttl = deltaSeconds(response.headers.get("cache-control"), "max-age");
      stored.set(request.url, clock.ms + (ttl ?? 0) * 1_000);
    },
    delete: async (request: Request) => stored.delete(request.url),
  } as unknown as Cache;
}

// A Cache double that also models the colo's WRITE-VISIBILITY LAG: a record is
// stored at once but does not read back for `lagMs`, which is the property the
// admission wait exists to outrun. ttlCache above is instantly consistent and so
// cannot show the herd at all.
function lagCache(clock: { ms: number }, lagMs: number): Cache {
  const stored = new Map<string, { visibleAt: number; until: number }>();
  return {
    match: async (request: Request) => {
      const record = stored.get(request.url);
      if (!record || clock.ms < record.visibleAt || clock.ms >= record.until) {
        return undefined;
      }
      return new Response(null);
    },
    put: async (request: Request, response: Response) => {
      const ttl = deltaSeconds(response.headers.get("cache-control"), "max-age");
      stored.set(request.url, {
        visibleAt: clock.ms + lagMs,
        until: clock.ms + (ttl ?? 0) * 1_000,
      });
    },
    delete: async (request: Request) => stored.delete(request.url),
  } as unknown as Cache;
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

// A release-gated origin: each call increments `calls` synchronously but
// blocks on `gate`, so a burst can be held with its leader mid-fill and its
// followers parked on the join. That's what makes the burst deterministic
// instead of depending on incidental Promise.all scheduling.
function gatedOrigin(respond: (call: number) => Promise<Response>) {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  const origin = (async () => {
    const call = ++origin.calls;
    await gate;
    return respond(call);
  }) as (() => Promise<Response>) & { calls: number; release: () => void };
  origin.calls = 0;
  origin.release = release;
  return origin;
}

// Lets the burst settle into its steady state — leader inside origin(),
// followers parked — before the gate is opened.
async function untilLeaderIsFilling(origin: { calls: number }) {
  while (origin.calls < 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

// A refresh thunk that records its invocations and reports how it ended: any
// RefreshOutcome, or "threw".
function countingRun(outcome: RefreshOutcome | "threw" = "landed") {
  const run = (async () => {
    run.calls++;
    if (outcome === "threw") throw new Error("refresh failed");
    return outcome;
  }) as (() => Promise<RefreshOutcome>) & { calls: number };
  run.calls = 0;
  return run;
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

  it("never puts the admission wait on the miss path, which the request is waiting on", async () => {
    // The wait belongs to a background refresh behind an already-served stale
    // response. The miss-path fill is on the serving path with joiners awaiting
    // it, so a wait there is up to a second of user-visible latency on every
    // cold miss — and a joiner cannot even be answered until it elapses, which
    // is what a wait that never elapses makes visible.
    const clock = { ms: 0 };
    const deps = { ...testDeps(clock), admissionDelay: () => new Promise<void>(() => {}) };
    const t = target("miss-undelayed", {
      refreshKey: "build:/miss-undelayed",
      revalidate: 60,
      expiration: 600,
    });
    const origin = gatedOrigin(async () =>
      new Response("rendered", { headers: { "cache-control": "s-maxage=60" } }),
    );

    const burst = Promise.all([
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
    ]);
    await untilLeaderIsFilling(origin);
    origin.release();
    const [leader, joiner] = await burst;

    expect(origin.calls).toBe(1);
    expect(leader!.headers.get("x-ocel-cache")).toBe("MISS");
    expect(joiner!.headers.get("x-ocel-cache")).toBe("HIT");
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

  it("strips the internal entry-modified header from the response returned to the browser, while still storing it", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    // Mimics a PRERENDER intercept hit from dispatch's cachingOrigin, which
    // stamps x-ocel-entry-modified (interception.ts) on the response it
    // returns to serveCached. That header must never reach the browser, but
    // forStorage still needs it off the cloned response to stamp the stored
    // object's real lastModified.
    const origin = (async () =>
      new Response("prerendered", {
        headers: {
          "cache-control": "s-maxage=60",
          "x-ocel-cache": "PRERENDER",
          "x-ocel-entry-modified": "12345",
        },
      })) as CountingOrigin;
    origin.calls = 0;

    const t = target("leak");
    const result = await serveCached(req(), t, deps, origin, origin);
    expect(result.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(result.headers.get("x-ocel-entry-modified")).toBeNull();
    await deps.flush();

    const stored = await caches.default.match(new Request(t.key));
    expect(stored?.headers.get("x-ocel-entry-modified")).toBe("12345");
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
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  it("hands the refresh the entry's remaining stale window, so the wait cannot outlive it", async () => {
    // A route whose stale window is shorter than the jitter would otherwise
    // spend the tail of the wait EXPIRED — and past expiration this tier
    // declines to serve at all, so L1 is bypassed and every isolate in the colo
    // renders for itself. The bound is what the draw is capped by; that it is
    // the remaining window and not the whole one is the whole of the fix.
    const clock = { ms: 0 };
    const bounds: number[] = [];
    const deps = {
      ...testDeps(clock),
      admissionDelay: (staleForMs: number) => {
        bounds.push(staleForMs);
        return Promise.resolve();
      },
    };
    const origin = countingOrigin("s-maxage=1, stale-while-revalidate=1");
    const refresh = countingOrigin("s-maxage=1, stale-while-revalidate=1");
    const t = target("short-stale", { refreshKey: "build:/short-stale" });

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 1_500; // past revalidate=1, 500ms left of expiration=2.
    expect((await serveCached(req(), t, deps, origin, refresh)).headers.get("x-ocel-cache")).toBe(
      "STALE",
    );
    await deps.flush();

    expect(bounds).toEqual([500]);
  });

  it("asks the tier below about the colo entry's own lastModified", async () => {
    const clock = { ms: 1_000 };
    const seen: number[] = [];
    const deps = testDeps(clock, caches.default, async (modified) => {
      seen.push(modified);
      return false;
    });
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");
    const t = target("below-modified", { refreshKey: "build:/below-modified" });

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    expect(seen).toEqual([1_000]);
  });

  it("leaves the wait unbounded when the entry declares no expiry", async () => {
    const clock = { ms: 0 };
    const bounds: number[] = [];
    const deps = {
      ...testDeps(clock),
      admissionDelay: (staleForMs: number) => {
        bounds.push(staleForMs);
        return Promise.resolve();
      },
    };
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");
    const t = target("no-expiry", { refreshKey: "build:/no-expiry" });

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    expect(bounds).toEqual([Infinity]);
  });

  // An on-demand ISR path — one generateStaticParams never named — has no
  // manifest window: Next emits initialRevalidate only for the paths it
  // prerendered at build. The window it does carry is the one Next stamps on
  // the response itself, so the entry must go stale on that rather than sit
  // fresh for the whole retention window.
  it("takes its window from the origin response when the manifest declares none", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1, stale-while-revalidate=59");
    const refresh = countingOrigin("s-maxage=1, stale-while-revalidate=59");
    const t = target("origin-window");

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000; // 5s old: past the response's own 1s window.
    const stale = await serveCached(req(), t, deps, origin, refresh);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  // The response describes what was actually rendered; the manifest only
  // describes what the build projected. A route whose window changed since the
  // build must not stay pinned to the build's number.
  it("prefers the origin response's window over the manifest's", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1, stale-while-revalidate=59");
    const refresh = countingOrigin("s-maxage=1, stale-while-revalidate=59");
    const t = target("window-precedence", { revalidate: 3600, expiration: 7200 });

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  // A bare s-maxage declares a revalidate window and nothing about expiry —
  // it is what the R2 tier synthesizes for an entry it serves. How long that
  // entry may still be served stale stays the manifest's to say.
  it("keeps the manifest expiration when the response declares no swr", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");
    const t = target("bare-smaxage", { revalidate: 1, expiration: 100 });

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000; // past revalidate=1, still inside the manifest's 100s.
    const stale = await serveCached(req(), t, deps, origin, refresh);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(origin.calls).toBe(1);
  });

  it("restates x-nextjs-cache by the freshness of the colo entry it serves", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    // The value stored with the entry: whatever the tier below reported when it
    // was written. A colo serve must not replay it — Next reports the freshness
    // of the entry it is answering with, so a stale replay of a HIT is a STALE.
    const origin = countingOrigin("s-maxage=1");
    const stamped = (async () => {
      const response = await origin();
      response.headers.set("x-nextjs-cache", "HIT");
      return response;
    }) as CountingOrigin;
    const t = target("nextjs-status", { revalidate: 1, expiration: 100 });

    await serveCached(req(), t, deps, stamped, stamped);
    await deps.flush();

    const fresh = await serveCached(req(), t, deps, stamped, stamped);
    expect(fresh.headers.get("x-ocel-cache")).toBe("HIT");
    expect(fresh.headers.get("x-nextjs-cache")).toBe("HIT");

    clock.ms = 5_000; // past revalidate=1, inside expiration=100
    const stale = await serveCached(req(), t, deps, stamped, stamped);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(stale.headers.get("x-nextjs-cache")).toBe("STALE");
    await deps.flush();
  });

  // The other half of self-revalidation suppression (bd ocelhq-wvag.26): with
  // `purpose: prefetch` on the user-path forward, a Lambda answering on a stale
  // entry serves it and starts no render. Those bytes are stale by construction
  // and must not become a colo entry that looks fresh for a whole window.
  describe("a STALE serve", () => {
    // Stamped like the Lambda's: Next's own header, and no x-ocel-* at all —
    // the control namespace is stripped inbound and only this worker writes it.
    const lambdaStale = (cacheControl = "s-maxage=60"): CountingOrigin => {
      const fn = (async () => {
        fn.calls++;
        return new Response("stale bytes", {
          headers: { "cache-control": cacheControl, "x-nextjs-cache": "STALE" },
        });
      }) as CountingOrigin;
      fn.calls = 0;
      return fn;
    };

    it("from the Lambda is served but not stored", async () => {
      const clock = { ms: 0 };
      const cache = countingCache();
      const deps = testDeps(clock, cache);
      const origin = lambdaStale();
      const t = target("lambda-stale");

      const res = await serveCached(req(), t, deps, origin, origin);
      await deps.flush();

      expect(await res.text()).toBe("stale bytes");
      expect(cache.puts).toBe(0);

      // And so the next request reaches the origin again rather than being
      // answered from a colo entry that would have read as fresh.
      await serveCached(req(), t, deps, origin, origin);
      expect(origin.calls).toBe(2);
    });

    // The narrowing that keeps the colo tier alive during a tag invalidation:
    // servedFromStore stamps STALE on Ocel's own R2 serves too, and gating on
    // the header alone would send every stale route back to R2 on every colo
    // miss for the whole duration of the invalidation.
    it("Ocel made from the R2 store is stored, because its provenance is not the Lambda", async () => {
      const clock = { ms: 0 };
      const cache = countingCache();
      const deps = testDeps(clock, cache);
      const origin = lambdaStale();
      const fromStore = (async () => {
        const response = await origin();
        response.headers.set("x-ocel-cache", "PRERENDER");
        return response;
      }) as CountingOrigin;
      const t = target("prerender-stale", { revalidate: 60, expiration: 600 });

      await serveCached(req(), t, deps, fromStore, fromStore);
      await deps.flush();

      expect(cache.puts).toBe(1);
      const hit = await serveCached(req(), t, deps, fromStore, fromStore);
      expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
      expect(origin.calls).toBe(1);
    });

    it.each(["HIT", "REVALIDATED"])(
      "is not what a %s response is, so it stores as before",
      async (status) => {
        const clock = { ms: 0 };
        const cache = countingCache();
        const deps = testDeps(clock, cache);
        const origin = countingOrigin("s-maxage=60");
        const stamped = (async () => {
          const response = await origin();
          response.headers.set("x-nextjs-cache", status);
          return response;
        }) as CountingOrigin;
        const t = target(`lambda-${status}`);

        await serveCached(req(), t, deps, stamped, stamped);
        await deps.flush();

        expect(cache.puts).toBe(1);
      },
    );
  });

  it("leaves a dynamic response unstamped: Next never sets x-nextjs-cache on one", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const origin = countingOrigin("s-maxage=60");
    const t = target("unstamped");

    await serveCached(req(), t, deps, origin, origin);
    await deps.flush();

    const hit = await serveCached(req(), t, deps, origin, origin);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(hit.headers.get("x-nextjs-cache")).toBeNull();
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
    expect(hit.headers.get("x-ocel-cache")).toBe("STALE");
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
    expect(hit.headers.get("x-ocel-cache")).toBe("STALE"); // served, not a miss
    await deps.flush();
    expect(refresh.calls).toBe(1);
  });

  it("collapses a burst of concurrent misses to a single origin render", async () => {
    const clock = { ms: 0 };
    const cache = countingCache();
    const deps = testDeps(clock, cache);
    const t = target("populate", { revalidate: 60, expiration: 600 });

    const origin = gatedOrigin(async () =>
      new Response("rendered", {
        status: 200,
        headers: { "cache-control": "s-maxage=60" },
      }),
    );

    const burst = Promise.all([
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
    ]);

    await untilLeaderIsFilling(origin);
    origin.release();

    const responses = await burst;
    await deps.flush();

    // Only the leader reached the origin; the followers were answered from the
    // entry it wrote, so they report HIT rather than MISS.
    expect(origin.calls).toBe(1);
    expect(cache.puts).toBe(1);
    expect(responses.map((res) => res.headers.get("x-ocel-cache"))).toEqual([
      "MISS",
      "HIT",
      "HIT",
    ]);
    for (const res of responses) {
      expect(res.status).toBe(200);
      expect(await res.text()).toBe("rendered");
    }

    const stored = await caches.default.match(new Request(t.key));
    expect(stored).not.toBeUndefined();
    // A second-generation read is a HIT: the single write is intact.
    clock.ms = 1_000;
    const hit = await serveCached(req(), t, deps, origin, origin);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
  });

  it("falls through to its own origin when the fill it joined stored nothing", async () => {
    const clock = { ms: 0 };
    const cache = countingCache();
    const deps = testDeps(clock, cache);
    const t = target("unstorable-leader", { revalidate: 60, expiration: 600 });

    const origin = gatedOrigin(async () =>
      new Response("uncacheable", {
        status: 200,
        headers: { "cache-control": "no-store" },
      }),
    );

    const burst = Promise.all([
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
    ]);

    await untilLeaderIsFilling(origin);
    origin.release();

    const responses = await burst;
    await deps.flush();

    // Nothing was stored, so there is no entry to answer the followers with:
    // each has to render for itself rather than go unanswered.
    expect(origin.calls).toBe(3);
    expect(cache.puts).toBe(0);
    for (const res of responses) {
      expect(res.status).toBe(200);
      expect(res.headers.get("x-ocel-cache")).toBe("MISS");
      expect(await res.text()).toBe("uncacheable");
    }
  });

  it("falls through to its own origin when the fill it joined rejected", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const t = target("rejecting-leader", { revalidate: 60, expiration: 600 });

    const origin = gatedOrigin(async (call) => {
      if (call === 1) throw new Error("origin unavailable");
      return new Response("rendered", {
        status: 200,
        headers: { "cache-control": "s-maxage=60" },
      });
    });

    const burst = Promise.allSettled([
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
      serveCached(req(), t, deps, origin, origin),
    ]);

    await untilLeaderIsFilling(origin);
    origin.release();

    const [leader, ...followers] = await burst;
    await deps.flush();

    // The leader's failure is its own; the followers neither hang on it nor
    // inherit it.
    expect(leader.status).toBe("rejected");
    expect(origin.calls).toBe(3);
    for (const settled of followers) {
      expect(settled.status).toBe("fulfilled");
      const res = (settled as PromiseFulfilledResult<Response>).value;
      expect(res.status).toBe(200);
      expect(res.headers.get("x-ocel-cache")).toBe("MISS");
      expect(await res.text()).toBe("rendered");
    }
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

  it("admits one refresh across every stale variant of the same route", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const refreshKey = "build:/variants";
    const html = target("variant-html", { revalidate: 1, refreshKey });
    const rsc = target("variant-rsc", { revalidate: 1, refreshKey });
    const htmlOrigin = countingOrigin("s-maxage=1", "html");
    const rscOrigin = countingOrigin("s-maxage=1", "rsc");
    const refresh = countingOrigin("s-maxage=1", "refreshed");

    await serveCached(req(), html, deps, htmlOrigin, refresh);
    await serveCached(req(), rsc, deps, rscOrigin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    const staleHtml = await serveCached(req(), html, deps, htmlOrigin, refresh);
    const staleRsc = await serveCached(req(), rsc, deps, rscOrigin, refresh);
    await deps.flush();

    // Each variant is still answered from its own entry...
    expect(staleHtml.headers.get("x-ocel-cache")).toBe("STALE");
    expect(staleRsc.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await staleHtml.text()).toBe("html");
    expect(await staleRsc.text()).toBe("rsc");
    // ...but one origin render rewrites the route, so only one was admitted.
    expect(refresh.calls).toBe(1);
  });

  it("keeps suppressing the route once the admitted refresh has settled", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const t = target("settled-refresh", {
      revalidate: 1,
      refreshKey: "build:/settled",
    });
    const origin = countingOrigin("s-maxage=1");
    const refresh = countingOrigin("s-maxage=1");

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();
    expect(refresh.calls).toBe(1);

    // The refresh rewrote the entry at 5_000 with a 1s window, so this request
    // is stale again — and nothing about it overlaps the first in flight. Only
    // the sentinel this colo already holds can suppress it.
    clock.ms = 6_500;
    const again = await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();
    expect(again.headers.get("x-ocel-cache")).toBe("STALE");
    expect(refresh.calls).toBe(1);
  });

  it("keeps the route's claim when the admitted refresh's origin refused it", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
    const t = target("errored-refresh", {
      revalidate: 1,
      refreshKey: "build:/errored",
    });
    const origin = countingOrigin("s-maxage=1");
    // A 500 stores nothing and throws nothing. It is also the signal that the
    // origin is shedding load, so re-admitting at once would aim the colo's
    // whole arrival rate at an origin that just said it could not cope.
    const refresh = countingOrigin("s-maxage=1", "boom", 500);

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();
    expect(refresh.calls).toBe(1);

    // Still stale, and still served stale — but the origin is not asked again
    // inside the backoff.
    clock.ms = 6_500;
    const again = await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();
    expect(again.headers.get("x-ocel-cache")).toBe("STALE");
    expect(refresh.calls).toBe(1);
  });
});

describe("serveCachedImage", () => {
  it("names no route, so it consults no sentinel and dedupes on the entry", async () => {
    const clock = { ms: 0 };
    const cache = recordingCache();
    const deps = testDeps(clock, cache);
    const one: CacheTarget = { key: "https://cache.ocel/image/one" };
    const two: CacheTarget = { key: "https://cache.ocel/image/two" };
    const origin = countingOrigin("max-age=1", "optimized");
    const refresh = countingOrigin("max-age=1", "reoptimized");

    await serveCachedImage(req(), one, deps, origin, refresh);
    await serveCachedImage(req(), two, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCachedImage(req(), one, deps, origin, refresh);
    await serveCachedImage(req(), one, deps, origin, refresh);
    await serveCachedImage(req(), two, deps, origin, refresh);
    await deps.flush();

    // Per-entry dedupe, exactly as before: one refresh each, not one between.
    expect(refresh.calls).toBe(2);
    // And no sentinel was ever consulted — not even under the entry keys, which
    // is what an image would fall back to admitting under.
    expect(cache.urls).not.toContain(sentinelUrl(one.key));
    expect(cache.urls).not.toContain(sentinelUrl(two.key));
  });
});

describe("admitRefresh", () => {
  it("suppresses the next admission of the same route within the sentinel's TTL", async () => {
    // The real workerd cache expires on real time, so the TTL boundary is
    // proven against a double that retains against the test's own clock.
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    const run = countingRun();

    admitRefresh(deps, "build:/ttl", 0, run);
    await deps.flush();
    expect(run.calls).toBe(1);

    clock.ms = refreshSentinelTtlSeconds * 1_000 - 1;
    admitRefresh(deps, "build:/ttl", 0, run);
    await deps.flush();
    expect(run.calls).toBe(1);

    clock.ms = refreshSentinelTtlSeconds * 1_000;
    admitRefresh(deps, "build:/ttl", 0, run);
    await deps.flush();
    expect(run.calls).toBe(2);
  });

  it("admits a different route while one route's sentinel stands", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    const run = countingRun();

    admitRefresh(deps, "build:/a", 0, run);
    await deps.flush();
    admitRefresh(deps, "build:/b", 0, run);
    await deps.flush();

    expect(run.calls).toBe(2);
  });

  it("releases the sentinel when the refresh throws, so the next request retries", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    const throwing = countingRun("threw");
    const succeeding = countingRun();

    admitRefresh(deps, "build:/failed", 0, throwing);
    await deps.flush();
    expect(throwing.calls).toBe(1);

    admitRefresh(deps, "build:/failed", 0, succeeding);
    await deps.flush();
    expect(succeeding.calls).toBe(1);
  });

  it("holds the claim for the backoff when the origin refused the refresh", async () => {
    // The throttle-amplification loop: a 429 or a 5xx used to release the claim
    // at once, so the colo re-admitted on the very next request and the failure
    // fed the herd it exists to damp.
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    const refused = countingRun("refused");

    admitRefresh(deps, "build:/refused", 0, refused);
    await deps.flush();
    expect(refused.calls).toBe(1);

    clock.ms = refreshBackoffSeconds * 1_000 - 1;
    admitRefresh(deps, "build:/refused", 0, refused);
    await deps.flush();
    expect(refused.calls).toBe(1);
  });

  it("re-admits once the backoff on a refusing origin has lapsed", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    const refused = countingRun("refused");

    admitRefresh(deps, "build:/recovers", 0, refused);
    await deps.flush();

    clock.ms = refreshBackoffSeconds * 1_000;
    admitRefresh(deps, "build:/recovers", 0, refused);
    await deps.flush();

    expect(refused.calls).toBe(2);
  });

  it("releases the sentinel when the refresh never reached the origin", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    const missed = countingRun("failed");
    const succeeding = countingRun();

    admitRefresh(deps, "build:/missed", 0, missed);
    await deps.flush();
    expect(missed.calls).toBe(1);

    admitRefresh(deps, "build:/missed", 0, succeeding);
    await deps.flush();
    expect(succeeding.calls).toBe(1);
  });

  it("measures the suppression from the refresh landing, not from the claim", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    // A 3s render: the claim is taken at 0 and the refresh lands at 3_000.
    const slow = (async () => {
      clock.ms += 3_000;
      return "landed" as const;
    }) as () => Promise<RefreshOutcome>;
    const next = countingRun();

    admitRefresh(deps, "build:/slow", 0, slow);
    await deps.flush();
    expect(clock.ms).toBe(3_000);

    // Past a TTL measured from the claim, still inside one measured from the
    // landing — the whole window the refresh's own duration would otherwise eat.
    clock.ms = 3_000 + refreshSentinelTtlSeconds * 1_000 - 1;
    admitRefresh(deps, "build:/slow", 0, next);
    await deps.flush();
    expect(next.calls).toBe(0);

    clock.ms = 3_000 + refreshSentinelTtlSeconds * 1_000;
    admitRefresh(deps, "build:/slow", 0, next);
    await deps.flush();
    expect(next.calls).toBe(1);
  });

  it("skips the render when the tier below already holds a fresher entry", async () => {
    // The whole point of the tier-below read: the origin call an admission
    // makes goes straight to the Function URL, so nothing else can tell this
    // colo that another one already regenerated the route.
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock), async () => true);
    const run = countingRun();

    admitRefresh(deps, "build:/already-fresh", 0, run);
    await deps.flush();

    expect(run.calls).toBe(0);
  });

  it("holds the claim for a full TTL when the tier below answered for it", async () => {
    // A refresh satisfied from below is a refresh that landed: the route is
    // fresh again, so the next admission inside the TTL has nothing to do.
    const clock = { ms: 0 };
    // False by the second admission, so the only thing that can suppress its
    // render is the claim the first one left behind.
    let below = true;
    const deps = testDeps(clock, ttlCache(clock), async () => below);
    const run = countingRun();

    admitRefresh(deps, "build:/held", 0, run);
    await deps.flush();

    below = false;
    clock.ms = refreshSentinelTtlSeconds * 1_000 - 1;
    admitRefresh(deps, "build:/held", 0, run);
    await deps.flush();

    expect(run.calls).toBe(0);
  });

  it("renders when the tier below is still stale", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock), async () => false);
    const run = countingRun();

    admitRefresh(deps, "build:/still-stale", 0, run);
    await deps.flush();

    expect(run.calls).toBe(1);
  });

  it("hands the tier below the lastModified of the entry being refreshed", async () => {
    // The tier below cannot judge "newer" without it, and a tier that only
    // answers "is it fresh?" strands a route whose window is shorter than the
    // round trip: its entry below is always stale by the time it is read.
    const clock = { ms: 0 };
    const seen: number[] = [];
    const deps = testDeps(clock, ttlCache(clock), async (modified) => {
      seen.push(modified);
      return false;
    });

    admitRefresh(deps, "build:/modified", 1_786_297_209_383, countingRun());
    await deps.flush();

    expect(seen).toEqual([1_786_297_209_383]);
  });

  it("renders when the tier-below read throws, never suppressing the refresh", async () => {
    // Fail open, as everywhere on this path: an unreadable tier below costs a
    // duplicate render, while treating it as fresh would cost the refresh.
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock), async () => {
      throw new Error("R2 exploded");
    });
    const run = countingRun();

    admitRefresh(deps, "build:/exploded", 0, run);
    await deps.flush();

    expect(run.calls).toBe(1);
  });

  it("consults the tier below only after the claim, so a suppressed admission reads nothing", async () => {
    const clock = { ms: 0 };
    const cache = ttlCache(clock);
    let reads = 0;
    const deps = testDeps(clock, cache, async () => {
      reads++;
      return false;
    });
    const run = countingRun();

    admitRefresh(deps, "build:/claimed", 0, run);
    await deps.flush();
    admitRefresh(deps, "build:/claimed", 0, run);
    await deps.flush();

    expect(run.calls).toBe(1);
    expect(reads).toBe(1);
  });

  it("admits every refresh when the colo cache is inert", async () => {
    // A domainless deploy answers on workers.dev, where caches.default accepts
    // every put and never hits: L1 must degrade to the per-isolate dedupe.
    const clock = { ms: 0 };
    const deps = testDeps(clock, {
      match: async () => undefined,
      put: async () => {},
      delete: async () => false,
    } as unknown as Cache);
    const run = countingRun();

    admitRefresh(deps, "build:/inert", 0, run);
    await deps.flush();
    admitRefresh(deps, "build:/inert", 0, run);
    await deps.flush();

    expect(run.calls).toBe(2);
  });

  it("admits when the cache itself throws", async () => {
    const clock = { ms: 0 };
    const thrower = {
      match: async () => {
        throw new Error("match exploded");
      },
      put: async () => {
        throw new Error("put exploded");
      },
      delete: async () => {
        throw new Error("delete exploded");
      },
    } as unknown as Cache;
    const deps = testDeps(clock, thrower);
    const run = countingRun();

    admitRefresh(deps, "build:/throwing", 0, run);
    await deps.flush();
    expect(run.calls).toBe(1);

    // And a throwing delete after a failed run neither escapes nor suppresses.
    const failing = countingRun("threw");
    admitRefresh(deps, "build:/throwing-failure", 0, failing);
    await deps.flush();
    expect(failing.calls).toBe(1);
  });

  it("waits before it claims, not after — an isolate evicted mid-wait leaves no sentinel", async () => {
    const clock = { ms: 0 };
    const cache = recordingCache();
    // A wait that never resolves stands in for the isolate going away while
    // holding the admission. Nothing may have been written by then, or the
    // route is suppressed for a whole TTL by a refresh that never ran.
    const deps = { ...testDeps(clock, cache), admissionDelay: () => new Promise<void>(() => {}) };
    const run = countingRun();

    admitRefresh(deps, "build:/evicted", 0, run);
    await Promise.resolve();
    await Promise.resolve();

    expect(cache.urls).toEqual([]);
    expect(run.calls).toBe(0);
  });

  it("collapses two admissions to one claim once they are more than a window apart", async () => {
    // The whole point of the wait. Against a cache that only becomes readable
    // W after the write — which is what the colo actually does — two claims
    // inside W do not see each other and both admit; drawn far enough apart,
    // the second sees the first. The spike measured W = 8ms.
    const admitAt = async (cache: Cache, clock: { ms: number }, draws: number[]) => {
      const run = countingRun();
      for (const draw of draws) {
        const deps = {
          ...testDeps(clock, cache),
          admissionDelay: async () => {
            clock.ms = draw;
          },
        };
        admitRefresh(deps, "build:/spread", 0, run);
        await deps.flush();
      }
      return run.calls;
    };

    const together = { ms: 0 };
    expect(await admitAt(lagCache(together, 8), together, [0, 4])).toBe(2);

    const apart = { ms: 0 };
    expect(await admitAt(lagCache(apart, 8), apart, [0, 900])).toBe(1);
  });

  it("keeps the per-isolate collapse across the wait, since it is keyed on the cache", async () => {
    // Load-bearing and non-obvious: inFlight is a WeakMap on deps.cache, not on
    // the deps object, so spreading CacheDeps to add a delay does not fragment
    // L0 — and L0 holding its entry across the wait is exactly what bounds the
    // claimant pool to the isolate count rather than to the arrival rate.
    const clock = { ms: 0 };
    const cache = ttlCache(clock);
    const run = countingRun();
    let release!: () => void;
    const held = new Promise<void>((resolve) => (release = resolve));
    const waiting = { ...testDeps(clock, cache), admissionDelay: () => held };
    const other = { ...testDeps(clock, cache), admissionDelay: () => Promise.resolve() };

    admitRefresh(waiting, "build:/shared", 0, run);
    admitRefresh(other, "build:/shared", 0, run);
    release();
    await waiting.flush();
    await other.flush();

    expect(run.calls).toBe(1);
  });

  it("draws inside the jitter, and inside the remaining stale window when that is shorter", () => {
    const random = vi.spyOn(Math, "random").mockReturnValue(0.9);
    try {
      expect(admissionDrawMs(Infinity)).toBe(0.9 * admissionJitterMs);
      expect(admissionDrawMs(10 * admissionJitterMs)).toBe(0.9 * admissionJitterMs);
      // The pathological route: 500ms of stale window left, so the draw is
      // 450ms rather than 900ms, and the refresh lands before the entry expires.
      expect(admissionDrawMs(500)).toBe(450);
      // A window already gone negative (a clock that moved under the read) must
      // draw zero, never a negative delay a timer would silently floor.
      expect(admissionDrawMs(-100)).toBe(0);
    } finally {
      random.mockRestore();
    }
  });

  it("draws its own delay when none is injected, neither zero nor unbounded", async () => {
    // Silent failure #4: a test double left wired in production would take the
    // whole colo's claims back inside one 8ms window, invisibly.
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock));
    delete (deps as CacheDeps).admissionDelay;
    const run = countingRun();
    const random = vi.spyOn(Math, "random").mockReturnValue(0.5);

    try {
      const started = Date.now();
      admitRefresh(deps, "build:/default", 0, run);
      expect(run.calls).toBe(0);
      await deps.flush();
      const elapsed = Date.now() - started;

      expect(random).toHaveBeenCalled();
      expect(run.calls).toBe(1);
      expect(elapsed).toBeGreaterThanOrEqual(admissionJitterMs / 2 - 20);
      expect(elapsed).toBeLessThan(admissionJitterMs);
    } finally {
      // In a finally, or a failure above pins Math.random at 0.5 for every test
      // after it in this file.
      random.mockRestore();
    }
  });

  it("admits when only put throws, having already seen the sentinel miss", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock, {
      match: async () => undefined,
      put: async () => {
        throw new Error("put exploded");
      },
      delete: async () => false,
    } as unknown as Cache);
    const run = countingRun();

    admitRefresh(deps, "build:/put-throws", 0, run);
    await deps.flush();

    expect(run.calls).toBe(1);
  });
});

describe("withVercelCacheAlias", () => {
  it("returns the response untouched when the build did not opt in", () => {
    const response = withStatus(new Response("body"), "HIT");

    const aliased = withVercelCacheAlias(response, undefined);

    expect(aliased).toBe(response);
    expect(aliased.headers.get("x-vercel-cache")).toBeNull();
  });

  it("emits no alias for a response no tier stamped a status on", () => {
    const response = new Response("body");

    const aliased = withVercelCacheAlias(response, true);

    expect(aliased).toBe(response);
    expect(aliased.headers.get("x-vercel-cache")).toBeNull();
  });

  it("copies the status verbatim, body and headers intact", async () => {
    const response = withStatus(
      new Response("body", { headers: { "content-type": "text/plain" } }),
      "STALE",
    );

    const aliased = withVercelCacheAlias(response, true);

    expect(aliased.headers.get("x-vercel-cache")).toBe("STALE");
    expect(aliased.headers.get("x-ocel-cache")).toBe("STALE");
    expect(aliased.headers.get("content-type")).toBe("text/plain");
    expect(await aliased.text()).toBe("body");
  });

  // The alias is stamped on the way out, never on what a tier stores: an entry
  // carrying it would outlive the build that asked for it.
  it("leaves the response the caller may still store unaliased", () => {
    const stored = withStatus(new Response("body"), "PRERENDER");

    withVercelCacheAlias(stored, true);

    expect(stored.headers.get("x-vercel-cache")).toBeNull();
  });
});
