import { describe, expect, it } from "vitest";
import { refreshHeader } from "@framework/next-cache";
import { deltaSeconds } from "@framework/next-router/http-cache";

import {
  refreshBackoffSeconds,
  refreshSentinelTtlSeconds,
  sentinelUrl,
  serveCached,
  type CacheDeps,
  type CacheTarget,
} from "../src/cache";
import { dispatchResult, type RouteDeps } from "../src/index";
import type { RevalidationMessage, RevalidationRoute } from "../src/revalidation";
import { parseMessage } from "@platform/edge-contract/revalidation";
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
        watch.ttls.push(
          deltaSeconds(response.headers.get("cache-control"), "max-age"),
        );
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

  it("re-arms the colo's claim on an accepted enqueue rather than releasing it", async () => {
    const sender = queue(true);
    const { cache } = await serveStale("re-armed", {
      deps: { enqueueRevalidation: sender.enqueueRevalidation },
    });

    expect(cache.ttls).toEqual([refreshSentinelTtlSeconds, refreshSentinelTtlSeconds]);
    expect(cache.deletes).toBe(0);
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

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

const cacheObject = (routePath: string) =>
  `${isrPrefix}/cache/${routePath}.cache.json`;

function storeOf(entries: Record<string, unknown>) {
  return {
    async get(key: string) {
      const entry = entries[key];
      return entry === undefined
        ? null
        : { text: async () => JSON.stringify(entry) };
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
      requests.filter(
        (r) => r.method === "GET" && r.headers.get("purpose") !== "prefetch",
      ),
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
      cache: {
        match: async () => undefined,
        put: async () => {},
      } as unknown as Cache,
      waitUntil: over.waitUntil ?? (() => {}),
      enqueueRevalidation: over.enqueueRevalidation,
    }),
    interception:
      "interception" in over
        ? over.interception
        : {
            config: { isrPrefix },
            now: () => over.now ?? 2_000,
            store: storeOf(
              over.entry ? { [cacheObject("blog")]: over.entry } : {},
            ),
          },
  };
}

const dispatchBlog = (deps: RouteDeps, request?: Request) =>
  dispatchResult(
    { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
    request ?? new Request("https://app.example/blog"),
    deps,
  );

async function dispatchStale(over: {
  entry?: unknown;
  enqueueRevalidation?: CacheDeps["enqueueRevalidation"];
  interception?: RouteDeps["interception"];
} = {}) {
  const pending: Promise<unknown>[] = [];
  const origin = recorder();
  const response = await dispatchBlog(
    blogDeps(origin, {
      entry: over.entry ?? storedEntry(1_000),
      now: 1_000 + 61_000,
      waitUntil: (p) => pending.push(p),
      enqueueRevalidation: over.enqueueRevalidation,
      ...("interception" in over ? { interception: over.interception } : {}),
    }),
  );
  await Promise.all(pending);
  return { response, origin };
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
          over.cache ??
          ({ match: async () => undefined, put: async () => {} } as unknown as Cache),
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
