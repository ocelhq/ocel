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

  it("is fresh before revalidate", () => {
    expect(evaluate(at(0, { revalidate: 60 }), 30_000)).toBe("fresh");
  });

  it("is stale once the revalidate window has passed", () => {
    expect(evaluate(at(0, { revalidate: 60 }), 120_000)).toBe("stale");
  });

  it("keeps an entry with no revalidate window fresh", () => {
    expect(evaluate(at(0, {}), 31_000_000_000)).toBe("fresh");
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
