import { refreshHeader } from "@framework/next-cache";
import { deltaSeconds } from "@framework/next-router/http-cache";
import { parseMessage } from "@platform/edge-contract/revalidation";
import { describe, expect, it } from "vitest";
import {
  type CacheDeps,
  type CacheTarget,
  refreshBackoffSeconds,
  refreshSentinelTtlSeconds,
  refreshThroughQueue,
  sentinelUrl,
  serveCached,
} from "../src/cache";
import { dispatchResult, type RouteDeps } from "../src/index";
import {
  queuedRefreshDeadlineMs,
  type RevalidationMessage,
  type RevalidationRoute,
  revalidationRetryWindowMs,
} from "../src/revalidation";
import { coloDeps } from "./cache-deps";

const isrPrefix = "prod/p/app/build";

const revalidation: RevalidationRoute = {
  headers: {
    "x-forwarded-host": "app.example",
    "x-forwarded-proto": "https",
  },
  expect: null,
  isrPrefix,
  routeId: "/blog",
  routePath: "/blog",
};

function queue(accept: boolean | "throws" = true) {
  const sent: RevalidationMessage[] = [];
  const enqueueRevalidation = async (message: RevalidationMessage) => {
    sent.push(message);
    if (accept === "throws") throw new Error("queue unreachable");
    return accept;
  };
  return { sent, enqueueRevalidation };
}

function sentinelWatch(key: string) {
  const real = caches.default;
  const url = sentinelUrl(key);
  const watch = {
    ttls: [] as (number | undefined)[],
    deletes: 0,
    match: (...args: Parameters<Cache["match"]>) => real.match(...args),
    put: (request: Request, response: Response) => {
      if (request.url === url) {
        watch.ttls.push(deltaSeconds(response.headers.get("cache-control"), "max-age"));
      }
      return real.put(request, response);
    },
    delete: (request: Request) => {
      if (request.url === url) watch.deletes++;
      return real.delete(request);
    },
  };
  return watch as unknown as Cache & { ttls: (number | undefined)[]; deletes: number };
}

function testDeps(
  clock: { ms: number },
  cache: Cache,
  over: Partial<CacheDeps> = {},
): CacheDeps & { flush: () => Promise<void> } {
  const pending: Promise<unknown>[] = [];
  return {
    ...coloDeps({
      cache,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
      ...over,
    }),
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
}

function countingOrigin(status = 200) {
  const fn = (async () => {
    fn.calls++;
    return new Response("rendered", {
      status,
      headers: { "cache-control": "s-maxage=1" },
    });
  }) as (() => Promise<Response>) & { calls: number };
  fn.calls = 0;
  return fn;
}

async function serveStale(
  name: string,
  over: { deps?: Partial<CacheDeps>; target?: Partial<CacheTarget>; status?: number } = {},
) {
  const key = `build:/${name}`;
  const cache = sentinelWatch(key);
  const clock = { ms: 0 };
  const deps = testDeps(clock, cache, over.deps);
  const target: CacheTarget = {
    key: `https://cache.ocel/enqueue/${name}`,
    refreshKey: key,
    revalidate: 1,
    expiration: 100,
    revalidation,
    ...over.target,
  };
  const request = new Request(`https://app.example/${name}`);
  const fill = countingOrigin();
  const blocking = countingOrigin(over.status);

  await serveCached(request, target, deps, fill, blocking);
  await deps.flush();
  clock.ms = 5_000;
  const stale = await serveCached(request, target, deps, fill, blocking);
  await deps.flush();

  return { stale, blocking, cache };
}

describe("the colo tier's admitted refresh", () => {
  it("hands an accepted refresh to the queue instead of rendering it", async () => {
    const sender = queue(true);
    const { stale, blocking } = await serveStale("accepted", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(blocking.calls).toBe(0);
    expect(sender.sent).toHaveLength(1);
  });

  it("holds the colo's claim for the retry window on an accepted enqueue", async () => {
    const sender = queue(true);
    const { cache } = await serveStale("re-armed", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(cache.ttls).toEqual([refreshSentinelTtlSeconds, revalidationRetryWindowMs / 1000]);
    expect(cache.deletes).toBe(0);
  });

  it("never counts a queued refresh as landed", async () => {
    const sender = queue(true);
    const deps = testDeps({ ms: 0 }, caches.default, {
      enqueueRevalidation: sender.enqueueRevalidation,
    });
    let direct = 0;

    const outcome = await refreshThroughQueue(
      deps,
      "build:/never-landed",
      revalidation,
      0,
      async () => {
        direct++;
        return "landed";
      },
    );

    expect(outcome).toBe("queued");
    expect(direct).toBe(0);
  });

  it("renders when the queue refuses the message", async () => {
    const sender = queue(false);
    const { blocking, cache } = await serveStale("refused", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(sender.sent).toHaveLength(1);
    expect(blocking.calls).toBe(1);
    expect(cache.ttls).toEqual([refreshSentinelTtlSeconds, refreshSentinelTtlSeconds]);
  });

  it("renders when the send rejects", async () => {
    const sender = queue("throws");
    const { blocking } = await serveStale("rejected", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(blocking.calls).toBe(1);
  });

  it("follows the origin's own verdict on the fallback render", async () => {
    const sender = queue(false);
    const { cache } = await serveStale("fallback-refused", {
      status: 500,
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(cache.ttls).toEqual([refreshSentinelTtlSeconds, refreshBackoffSeconds]);
  });

  it("sends nothing when the tier below already answered the refresh", async () => {
    const sender = queue(true);
    const { blocking } = await serveStale("from-below", {
      deps: {
        enqueueRevalidation: sender.enqueueRevalidation,
        satisfiedFromBelow: async () => true,
      },
    });

    expect(sender.sent).toEqual([]);
    expect(blocking.calls).toBe(0);
  });

  it("renders exactly as before when no queue is bound", async () => {
    const { blocking, cache } = await serveStale("unbound");

    expect(blocking.calls).toBe(1);
    expect(cache.ttls).toEqual([refreshSentinelTtlSeconds, refreshSentinelTtlSeconds]);
  });

  it("renders when the route names nothing the consumer could resolve", async () => {
    const sender = queue(true);
    const { blocking } = await serveStale("no-route", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
      target: { revalidation: undefined },
    });

    expect(sender.sent).toEqual([]);
    expect(blocking.calls).toBe(1);
  });

  it("names the entry generation the staleness verdict was taken on", async () => {
    const sender = queue(true);
    await serveStale("generation", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(sender.sent[0].lastModified).toBe(0);
  });
});

async function staleAt(name: string, enqueueRevalidation: CacheDeps["enqueueRevalidation"]) {
  const key = `build:/${name}`;
  const clock = { ms: 0 };
  const deps = testDeps(clock, caches.default, { enqueueRevalidation });
  const target: CacheTarget = {
    key: `https://cache.ocel/enqueue/${name}`,
    refreshKey: key,
    revalidate: 1,
    expiration: 100_000,
    revalidation,
  };
  const request = new Request(`https://app.example/${name}`);
  const blocking = countingOrigin();
  await serveCached(request, target, deps, countingOrigin(), blocking);
  await deps.flush();
  clock.ms = 5_000;
  return {
    clock,
    blocking,
    expireSentinel: () => caches.default.delete(new Request(sentinelUrl(key))),
    hit: async () => {
      const response = await serveCached(request, target, deps, countingOrigin(), blocking);
      await deps.flush();
      return response;
    },
  };
}

describe("a queued refresh that stays stale", () => {
  it("enqueues a stale entry's refresh at most once per retry window", async () => {
    const sender = queue(true);
    const run = await staleAt("once-per-window", sender.enqueueRevalidation);

    await run.hit();
    await run.hit();
    expect(sender.sent).toHaveLength(1);

    await run.expireSentinel();
    await run.hit();
    expect(sender.sent).toHaveLength(2);
    expect(run.blocking.calls).toBe(0);
  });

  it("renders a queued refresh itself once the queue has not landed it within five minutes", async () => {
    const sender = queue(true);
    const run = await staleAt("deadline", sender.enqueueRevalidation);

    await run.hit();
    run.clock.ms += queuedRefreshDeadlineMs;
    await run.expireSentinel();
    await run.hit();

    expect(run.blocking.calls).toBe(1);
    expect(sender.sent).toHaveLength(1);
  });

  it("keeps waiting on the queue until the five minutes pass", async () => {
    const sender = queue(true);
    const run = await staleAt("before-deadline", sender.enqueueRevalidation);

    await run.hit();
    run.clock.ms += queuedRefreshDeadlineMs - 1_000;
    await run.expireSentinel();
    await run.hit();

    expect(run.blocking.calls).toBe(0);
    expect(sender.sent).toHaveLength(2);
  });

  it("starts the five minutes again for a newer entry", async () => {
    const sender = queue(true);
    const deps = testDeps({ ms: 0 }, caches.default, {
      enqueueRevalidation: sender.enqueueRevalidation,
    });
    const clock = { ms: 0 };
    const withClock = { ...deps, now: () => clock.ms };
    let direct = 0;
    const render = async () => {
      direct++;
      return "landed" as const;
    };
    const key = "build:/newer-entry";

    await refreshThroughQueue(withClock, key, revalidation, 0, render);
    clock.ms = queuedRefreshDeadlineMs - 1;
    await refreshThroughQueue(withClock, key, revalidation, 400_000, render);
    clock.ms = queuedRefreshDeadlineMs + 1_000;
    const outcome = await refreshThroughQueue(withClock, key, revalidation, 400_000, render);

    expect(outcome).toBe("queued");
    expect(direct).toBe(0);
    expect(sender.sent).toHaveLength(3);
  });
});

describe("a queued refresh's deadline", () => {
  it("holds when the colo sees the entry less often than the marker's retry window", async () => {
    const sender = queue(true);
    const clock = { ms: 0 };
    const stored = new Map<string, { expires: number; response: Response }>();
    const cache = {
      match: async (request: Request) => {
        const hit = stored.get(request.url);
        if (!hit || hit.expires <= clock.ms) return undefined;
        return hit.response.clone();
      },
      put: async (request: Request, response: Response) => {
        const seconds = deltaSeconds(response.headers.get("cache-control"), "max-age") ?? 0;
        stored.set(request.url, { expires: clock.ms + seconds * 1_000, response });
      },
      delete: async (request: Request) => stored.delete(request.url),
    } as unknown as Cache;
    const deps = { ...testDeps(clock, cache, { enqueueRevalidation: sender.enqueueRevalidation }) };
    let direct = 0;
    const render = async () => {
      direct++;
      return "landed" as const;
    };

    await refreshThroughQueue(deps, "build:/rare", revalidation, 0, render);
    clock.ms += 400_000;
    const outcome = await refreshThroughQueue(deps, "build:/rare", revalidation, 0, render);

    expect(outcome).toBe("landed");
    expect(direct).toBe(1);
    expect(sender.sent).toHaveLength(1);
  });

  it("renders once per colo when several isolates find it overdue at the same time", async () => {
    const sender = queue(true);
    const key = "https://cache.ocel/enqueue/overdue-race";
    const shared = await caches.open("overdue-race");
    const clock = { ms: 0 };
    let readers = 0;
    let release!: () => void;
    const allRead = new Promise<void>((resolve) => {
      release = resolve;
    });
    const isolate = (arrival = 0) =>
      testDeps(
        clock,
        {
          match: async (request: Request) => {
            if (new URL(request.url).host === "queued.refresh.ocel" && clock.ms > 5_000) {
              if (++readers === 3) release();
              await allRead;
            }
            if (new URL(request.url).host === "refresh.ocel") {
              await new Promise((done) => setTimeout(done, arrival));
            }
            return shared.match(request);
          },
          put: (request: Request, response: Response) => shared.put(request, response),
          delete: (request: Request) => shared.delete(request),
        } as unknown as Cache,
        { enqueueRevalidation: sender.enqueueRevalidation },
      );
    const target: CacheTarget = {
      key,
      revalidate: 1,
      expiration: 100_000,
      revalidation,
    };
    const request = new Request("https://app.example/overdue-race");
    const blocking = countingOrigin();
    const first = isolate();
    await serveCached(request, target, first, countingOrigin(), blocking);
    await first.flush();
    clock.ms = 5_000;
    await serveCached(request, target, first, countingOrigin(), blocking);
    await first.flush();
    expect(sender.sent).toHaveLength(1);

    clock.ms += queuedRefreshDeadlineMs;
    const isolates = [isolate(0), isolate(25), isolate(50)];
    readers = 0;
    await Promise.all(
      isolates.map((deps) => serveCached(request, target, deps, countingOrigin(), blocking)),
    );
    await Promise.all(isolates.map((deps) => deps.flush()));

    expect(blocking.calls).toBe(1);
  });
});

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

const cacheObject = (routePath: string) => `${isrPrefix}/cache/${routePath}.cache.json`;

function storeOf(entries: Record<string, unknown>) {
  return {
    async get(key: string) {
      const entry = entries[key];
      return entry === undefined ? null : { text: async () => JSON.stringify(entry) };
    },
  };
}

function recorder() {
  const requests: Request[] = [];
  const fetch = (async (request: Request) => {
    requests.push(request);
    return new Response("from-lambda", {
      status: 200,
      headers: { "cache-control": "s-maxage=60" },
    });
  }) as unknown as typeof fetch;
  return {
    requests,
    fetch,
    revalidating: () =>
      requests.filter((r) => r.method === "GET" && r.headers.get("purpose") !== "prefetch"),
  };
}

const storedEntry = (lastModified: number, over: Record<string, unknown> = {}) => ({
  lastModified,
  value: {
    kind: "APP_PAGE",
    html: "<html>from-store</html>",
    status: 200,
    headers: {},
    ...over,
  },
});

function blogDeps(
  origin: ReturnType<typeof recorder>,
  over: {
    entry?: unknown;
    now?: number;
    cache?: Cache;
    cacheNow?: () => number;
    enqueueRevalidation?: CacheDeps["enqueueRevalidation"];
    waitUntil?: (p: Promise<unknown>) => void;
    interception?: RouteDeps["interception"];
  } = {},
): RouteDeps {
  return {
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {
        "/blog": {
          kind: "prerender" as const,
          id: "bundle-0",
          entryKey: "app/blog/page",
          config: { allowHeader: ["host"], bypassToken: "TOKEN" },
          fallback: { initialRevalidate: 60 },
        },
      },
    },
    functionUrls: { "bundle-0": "https://fn.example.com" },
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: noAssets(),
    fetch: origin.fetch,
    cache: coloDeps({
      cache:
        over.cache ??
        ({
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache),
      now: over.cacheNow,
      waitUntil: over.waitUntil ?? (() => {}),
      enqueueRevalidation: over.enqueueRevalidation,
    }),
    interception:
      "interception" in over
        ? over.interception
        : {
            config: { isrPrefix },
            now: () => over.now ?? 2_000,
            store: storeOf(over.entry ? { [cacheObject("blog")]: over.entry } : {}),
          },
  };
}

const dispatchBlog = (deps: RouteDeps, request?: Request) =>
  dispatchResult(
    { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
    request ?? new Request("https://app.example/blog"),
    deps,
  );

async function dispatchStale(
  over: {
    entry?: unknown;
    cache?: Cache;
    cacheNow?: () => number;
    enqueueRevalidation?: CacheDeps["enqueueRevalidation"];
    interception?: RouteDeps["interception"];
  } = {},
) {
  const pending: Promise<unknown>[] = [];
  const origin = recorder();
  const response = await dispatchBlog(
    blogDeps(origin, {
      entry: over.entry ?? storedEntry(1_000),
      now: 1_000 + 61_000,
      waitUntil: (p) => pending.push(p),
      cache: over.cache,
      cacheNow: over.cacheNow,
      enqueueRevalidation: over.enqueueRevalidation,
      ...("interception" in over ? { interception: over.interception } : {}),
    }),
  );
  await Promise.all(pending);
  return { response, origin };
}

async function queuedThenOverdue(name: string, entry?: unknown) {
  const sender = queue(true);
  const clock = { ms: 0 };
  const real = await caches.open(`queued-${name}`);
  const sentinels = new Set<string>();
  const cache = {
    match: (request: Request) => real.match(request),
    delete: (request: Request) => real.delete(request),
    put: (request: Request, response: Response) => {
      if (new URL(request.url).host === "refresh.ocel") sentinels.add(request.url);
      return real.put(request, response);
    },
  } as unknown as Cache;
  const run = () =>
    dispatchStale({
      entry,
      cache,
      cacheNow: () => clock.ms,
      enqueueRevalidation: sender.enqueueRevalidation,
    });
  await run();
  clock.ms += queuedRefreshDeadlineMs;
  for (const url of sentinels) await real.delete(new Request(url));
  const second = await run();
  return { sender, second };
}

describe("the R2 tier's admitted refresh", () => {
  it("hands an accepted refresh to the queue instead of rendering it", async () => {
    const sender = queue(true);
    const { response, origin } = await dispatchStale({
      enqueueRevalidation: sender.enqueueRevalidation,
    });

    expect(response.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(origin.revalidating()).toEqual([]);
    expect(sender.sent).toHaveLength(1);
  });

  it("renders a queued refresh itself once the queue has not landed it within five minutes", async () => {
    const { sender, second } = await queuedThenOverdue("r2");

    expect(second.origin.revalidating()).toHaveLength(1);
    expect(sender.sent).toHaveLength(1);
  });

  it("renders when the queue refuses the message", async () => {
    const sender = queue(false);
    const { origin } = await dispatchStale({
      enqueueRevalidation: sender.enqueueRevalidation,
    });

    expect(origin.revalidating()).toHaveLength(1);
  });

  it("renders exactly as before when no queue is bound", async () => {
    const { origin } = await dispatchStale();

    expect(origin.revalidating()).toHaveLength(1);
  });

  it("builds the message from what the route knows", async () => {
    const sender = queue(true);
    await dispatchStale({ enqueueRevalidation: sender.enqueueRevalidation });

    expect(sender.sent[0]).toEqual({
      v: 1,
      headers: {
        "x-ocel-entry": "app/blog/page",
        "x-forwarded-host": "app.example",
        "x-forwarded-proto": "https",
        [refreshHeader]: "1000",
      },
      expect: null,
      isrPrefix,
      routeId: "bundle-0",
      routePath: "/blog",
      lastModified: 1_000,
      enqueuedAt: expect.any(Number),
    });
  });

  it("builds a message the consumer's own parser accepts", async () => {
    const sender = queue(true);
    await dispatchStale({ enqueueRevalidation: sender.enqueueRevalidation });

    const parsed = parseMessage(JSON.stringify(sender.sent[0]));
    expect(parsed).toEqual({ ok: true, message: sender.sent[0] });
  });

  it("sends nothing where no ISR tier can resolve the route", async () => {
    const sender = queue(true);
    const origin = recorder();
    const pending: Promise<unknown>[] = [];

    await dispatchBlog(
      blogDeps(origin, {
        interception: undefined,
        enqueueRevalidation: sender.enqueueRevalidation,
        waitUntil: (p) => pending.push(p),
      }),
    );
    await Promise.all(pending);

    expect(sender.sent).toEqual([]);
  });
});

describe("a PPR route's admitted refresh", () => {
  const pprEntry = storedEntry(1_000, { postponed: "POSTPONED" });

  it("hands an accepted refresh to the queue instead of rendering it", async () => {
    const sender = queue(true);
    const { origin } = await dispatchStale({
      entry: pprEntry,
      enqueueRevalidation: sender.enqueueRevalidation,
    });

    expect(origin.revalidating()).toEqual([]);
    expect(origin.requests).toHaveLength(1);
    expect(sender.sent).toHaveLength(1);
  });

  it("renders a queued refresh itself once the queue has not landed it within five minutes", async () => {
    const { sender, second } = await queuedThenOverdue("ppr", pprEntry);

    expect(second.origin.revalidating()).toHaveLength(1);
    expect(sender.sent).toHaveLength(1);
  });

  it("renders when the queue refuses the message", async () => {
    const sender = queue(false);
    const { origin } = await dispatchStale({
      entry: pprEntry,
      enqueueRevalidation: sender.enqueueRevalidation,
    });

    expect(origin.revalidating()).toHaveLength(1);
  });

  it("renders exactly as before when no queue is bound", async () => {
    const { origin } = await dispatchStale({ entry: pprEntry });

    expect(origin.revalidating()).toHaveLength(1);
  });

  it("names the entry generation the staleness verdict was taken on", async () => {
    const sender = queue(true);
    await dispatchStale({
      entry: pprEntry,
      enqueueRevalidation: sender.enqueueRevalidation,
    });

    expect(sender.sent[0].lastModified).toBe(1_000);
  });
});

describe("revalidating a fallback path of a dynamic route", () => {
  function fallbackDeps(
    origin: ReturnType<typeof recorder>,
    over: {
      entries?: Record<string, unknown>;
      now?: number;
      enqueueRevalidation?: CacheDeps["enqueueRevalidation"];
      waitUntil?: (p: Promise<unknown>) => void;
      cache?: Cache;
    } = {},
  ): RouteDeps {
    return {
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog/[slug]": {
            kind: "prerender" as const,
            id: "bundle-0",
            entryKey: "app/blog/[slug]/page",
            config: { allowHeader: ["host"], bypassToken: "TOKEN" },
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "bundle-0": "https://fn.example.com" },
      slug: "p1",
      deploymentId: "d1",
      app: "web",
      assetStore: noAssets(),
      fetch: origin.fetch,
      cache: coloDeps({
        cache:
          over.cache ?? ({ match: async () => undefined, put: async () => {} } as unknown as Cache),
        waitUntil: over.waitUntil ?? (() => {}),
        enqueueRevalidation: over.enqueueRevalidation,
      }),
      interception: {
        config: { isrPrefix },
        now: () => over.now ?? 2_000,
        store: storeOf(over.entries ?? {}),
      },
    };
  }

  const dispatchFallback = (deps: RouteDeps, path: string) =>
    dispatchResult(
      {
        resolvedPathname: "/blog/[slug]",
        routePath: path,
        invocationTarget: { pathname: path },
      },
      new Request(`https://app.example${path}`),
      deps,
    );

  it("sends the concrete requested path as the revalidation's routePath, not the dynamic pattern", async () => {
    const sender = queue(true);
    const pending: Promise<unknown>[] = [];
    await dispatchFallback(
      fallbackDeps(recorder(), {
        entries: { [cacheObject("blog/three")]: storedEntry(1_000) },
        now: 1_000 + 61_000,
        enqueueRevalidation: sender.enqueueRevalidation,
        waitUntil: (p) => pending.push(p),
      }),
      "/blog/three",
    );
    await Promise.all(pending);

    expect(sender.sent).toHaveLength(1);
    expect(sender.sent[0].routePath).toBe("/blog/three");
  });

  it("reads the intercept target at the concrete path, keeping the pattern as the fallback shell key", async () => {
    const res = await dispatchFallback(
      fallbackDeps(recorder(), {
        entries: {
          [cacheObject("blog/three")]: storedEntry(1_000, { html: "<html>three</html>" }),
          [cacheObject("blog/[slug]")]: storedEntry(1_000, {
            html: "<html>pattern-shell</html>",
          }),
        },
      }),
      "/blog/three",
    );

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>three</html>");
  });

  it("gives two different slugs of the same dynamic route different refresh keys", async () => {
    const store = new Map<string, Response>();
    const sentinelCache = {
      match: async (req: Request) => store.get(req.url),
      put: async (req: Request, res: Response) => {
        store.set(req.url, res);
      },
      delete: async (req: Request) => store.delete(req.url),
    } as unknown as Cache;

    const sender = queue(true);

    const pendingOne: Promise<unknown>[] = [];
    await dispatchFallback(
      fallbackDeps(recorder(), {
        entries: { [cacheObject("blog/one")]: storedEntry(1_000) },
        now: 1_000 + 61_000,
        enqueueRevalidation: sender.enqueueRevalidation,
        waitUntil: (p) => pendingOne.push(p),
        cache: sentinelCache,
      }),
      "/blog/one",
    );
    await Promise.all(pendingOne);

    const pendingTwo: Promise<unknown>[] = [];
    await dispatchFallback(
      fallbackDeps(recorder(), {
        entries: { [cacheObject("blog/two")]: storedEntry(1_000) },
        now: 1_000 + 61_000,
        enqueueRevalidation: sender.enqueueRevalidation,
        waitUntil: (p) => pendingTwo.push(p),
        cache: sentinelCache,
      }),
      "/blog/two",
    );
    await Promise.all(pendingTwo);

    expect(sender.sent).toHaveLength(2);
    expect(sender.sent.map((m) => m.routePath)).toEqual(["/blog/one", "/blog/two"]);
  });

  it("leaves a concrete prerendered path (no dynamic fallback) unaffected", async () => {
    const sender = queue(true);
    await dispatchStale({ enqueueRevalidation: sender.enqueueRevalidation });

    expect(sender.sent[0].routePath).toBe("/blog");
  });
});
