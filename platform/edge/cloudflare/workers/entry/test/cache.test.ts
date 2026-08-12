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

function testDeps(
  clock: { ms: number },
  cache: Cache = caches.default,
  satisfiedFromBelow?: CacheDeps["satisfiedFromBelow"],
): CacheDeps & {
  flush: () => Promise<void>;
} {
  const pending: Promise<unknown>[] = [];
  return {
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

async function untilLeaderIsFilling(origin: { calls: number }) {
  while (origin.calls < 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

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

  it("folds next-url into a segment prefetch's path so an intercepted and a plain navigation to the same URL cannot collide", () => {
    const plain = variantPath(
      "/photo",
      H({ RSC: "1", "next-router-segment-prefetch": "/children" }),
      "PARTIALLY_STATIC",
    );
    const intercepted = variantPath(
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
    expect(variantPath("/photo", withUrl, "PARTIALLY_STATIC")).not.toBe(
      variantPath("/photo", withoutUrl, "PARTIALLY_STATIC"),
    );

    const staticWithUrl = H({ RSC: "1", "next-url": "/feed" });
    const staticWithoutUrl = H({ RSC: "1" });
    expect(variantPath("/photo", staticWithUrl, "STATIC")).not.toBe(
      variantPath("/photo", staticWithoutUrl, "STATIC"),
    );
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

  it("gives an intercepted segment prefetch a different colo key than the same URL without next-url", () => {
    const url = new URL("https://app.example/photo");
    const headers = { RSC: "1", "next-router-segment-prefetch": "/children" };
    const plain = cacheKey("b", "/photo", url, H(headers), "PARTIALLY_STATIC", []);
    const intercepted = cacheKey(
      "b",
      "/photo",
      url,
      H({ ...headers, "next-url": "/feed" }),
      "PARTIALLY_STATIC",
      [],
    );

    expect(plain).not.toEqual(intercepted);
  });
});

describe("serveCached", () => {
  const target = (
    name: string,
    over: Partial<CacheTarget> = {},
  ): CacheTarget => ({
    key: `https://cache.ocel/build/${name}`,
    ...over,
  });

  it("never puts the admission wait on the miss path, which the request is waiting on", async () => {
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

  it("answers a joiner whose leader's store never settles", async () => {
    const clock = { ms: 0 };
    const stalled = {
      match: async () => undefined,
      put: () => new Promise<void>(() => {}),
      delete: async () => false,
    } as unknown as Cache;
    const deps = { ...testDeps(clock, stalled), joinFillTimeoutMs: 50 };
    const t = target("stalled-store", { refreshKey: "build:/stalled-store" });
    const origin = countingOrigin("s-maxage=60");

    const leader = await serveCached(req(), t, deps, origin, origin);
    expect(leader.headers.get("x-ocel-cache")).toBe("MISS");

    const joiner = await Promise.race([
      serveCached(req(), t, deps, origin, origin),
      new Promise<"stranded">((resolve) =>
        setTimeout(() => resolve("stranded"), 1_000),
      ),
    ]);

    expect(joiner).not.toBe("stranded");
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
    const origin = (async () =>
      new Response("prerendered", {
        headers: { "cache-control": "s-maxage=60", "x-ocel-cache": "PRERENDER" },
      })) as CountingOrigin;
    origin.calls = 0;

    const first = await serveCached(req(), target("edge"), deps, origin, origin);
    expect(first.headers.get("x-ocel-cache")).toBe("PRERENDER");
    await deps.flush();

    clock.ms = 1_000;
    const second = await serveCached(req(), target("edge"), deps, origin, origin);
    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
  });

  it("strips the internal entry-modified header from the response returned to the browser, while still storing it", async () => {
    const clock = { ms: 0 };
    const deps = testDeps(clock);
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

  describe("a STALE serve", () => {
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

      await serveCached(req(), t, deps, origin, origin);
      expect(origin.calls).toBe(2);
    });

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

    expect(staleHtml.headers.get("x-ocel-cache")).toBe("STALE");
    expect(staleRsc.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await staleHtml.text()).toBe("html");
    expect(await staleRsc.text()).toBe("rsc");
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
    const refresh = countingOrigin("s-maxage=1", "boom", 500);

    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();

    clock.ms = 5_000;
    await serveCached(req(), t, deps, origin, refresh);
    await deps.flush();
    expect(refresh.calls).toBe(1);

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

    expect(refresh.calls).toBe(2);
    expect(cache.urls).not.toContain(sentinelUrl(one.key));
    expect(cache.urls).not.toContain(sentinelUrl(two.key));
  });
});

describe("admitRefresh", () => {
  it("suppresses the next admission of the same route within the sentinel's TTL", async () => {
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
    const slow = (async () => {
      clock.ms += 3_000;
      return "landed" as const;
    }) as () => Promise<RefreshOutcome>;
    const next = countingRun();

    admitRefresh(deps, "build:/slow", 0, slow);
    await deps.flush();
    expect(clock.ms).toBe(3_000);

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
    const clock = { ms: 0 };
    const deps = testDeps(clock, ttlCache(clock), async () => true);
    const run = countingRun();

    admitRefresh(deps, "build:/already-fresh", 0, run);
    await deps.flush();

    expect(run.calls).toBe(0);
  });

  it("holds the claim for a full TTL when the tier below answered for it", async () => {
    const clock = { ms: 0 };
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

    const failing = countingRun("threw");
    admitRefresh(deps, "build:/throwing-failure", 0, failing);
    await deps.flush();
    expect(failing.calls).toBe(1);
  });

  it("waits before it claims, not after — an isolate evicted mid-wait leaves no sentinel", async () => {
    const clock = { ms: 0 };
    const cache = recordingCache();
    const deps = { ...testDeps(clock, cache), admissionDelay: () => new Promise<void>(() => {}) };
    const run = countingRun();

    admitRefresh(deps, "build:/evicted", 0, run);
    await Promise.resolve();
    await Promise.resolve();

    expect(cache.urls).toEqual([]);
    expect(run.calls).toBe(0);
  });

  it("collapses two admissions to one claim once they are more than a window apart", async () => {
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
      expect(admissionDrawMs(500)).toBe(450);
      expect(admissionDrawMs(-100)).toBe(0);
    } finally {
      random.mockRestore();
    }
  });

  it("draws its own delay when none is injected, neither zero nor unbounded", async () => {
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

  it("leaves the response the caller may still store unaliased", () => {
    const stored = withStatus(new Response("body"), "PRERENDER");

    withVercelCacheAlias(stored, true);

    expect(stored.headers.get("x-vercel-cache")).toBeNull();
  });
});
