import { describe, expect, it } from "vitest";
import {
  type CacheDeps,
  type CacheTarget,
  type EntryMeta,
  evaluate,
  serveCachedImage,
} from "../src/cache";

function testDeps(
  clock: { ms: number },
  cache: Cache = caches.default,
): CacheDeps & {
  flush: () => Promise<void>;
} {
  const pending: Promise<unknown>[] = [];
  return {
    cache,
    now: () => clock.ms,
    waitUntil: (promise) => {
      pending.push(promise);
    },
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
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

type CountingOrigin = (() => Promise<Response>) & { calls: number };

function countingOrigin(cacheControl: string, body = "rendered", status = 200): CountingOrigin {
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

const req = (url = "https://app.example/", init?: RequestInit) => new Request(url, init);

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

describe("serveCachedImage", () => {
  it("dedupes a stale refresh on the entry", async () => {
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
    expect(cache.urls.every((url) => url === one.key || url === two.key)).toBe(true);
  });
});
