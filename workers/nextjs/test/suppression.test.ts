import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";
import { coloDeps } from "./cache-deps";

// Self-revalidation suppression (bd ocelhq-wvag.26): the edge stamps
// `purpose: prefetch` on the forward a user request makes to the Lambda, which
// makes Next serve a stale entry without starting its own revalidating render.
// The edge's admission tiers are then the only thing that can start one.
//
// Everything asserted here is about WHICH forward carries it. The header on the
// wrong leg is not a visible failure — it is a route that silently stops
// revalidating — so each leg gets its own named test.
const PREFETCH = "purpose";

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

function missingCache(waitUntil: (p: Promise<unknown>) => void = () => {}) {
  return coloDeps({
    cache: {
      match: async () => undefined,
      put: async () => {},
    } as unknown as Cache,
    waitUntil,
  });
}

const isrPrefix = "prod/p/app/build";
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
    purposes: () => requests.map((r) => r.headers.get(PREFETCH)),
  };
}

const target = {
  kind: "prerender" as const,
  id: "/blog",
  config: {
    allowHeader: ["host", "purpose"],
    bypassFor: [{ type: "cookie" as const, key: "preview" }],
    bypassToken: "TOKEN",
  },
  fallback: { initialRevalidate: 60 },
};

function blogDeps(
  origin: ReturnType<typeof recorder>,
  over: {
    entry?: unknown;
    now?: number;
    waitUntil?: (p: Promise<unknown>) => void;
    cache?: RouteDeps["cache"];
    interception?: RouteDeps["interception"];
  } = {},
): RouteDeps {
  return {
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: { "/blog": target },
    },
    functionUrls: { "/blog": "https://fn.example.com" },
    slug: "p1",
    app: "web",
    assetStore: noAssets(),
    fetch: origin.fetch,
    cache: "cache" in over ? over.cache : missingCache(over.waitUntil),
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

const storedEntry = (lastModified: number) => ({
  lastModified,
  value: {
    kind: "APP_PAGE",
    html: "<html>from-store</html>",
    status: 200,
    headers: {},
  },
});

const dispatchBlog = (deps: RouteDeps, request?: Request) =>
  dispatchResult(
    { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
    request ?? new Request("https://app.example/blog"),
    deps,
  );

describe("self-revalidation suppression", () => {
  it("stamps purpose: prefetch on the user-path forward to the Lambda", async () => {
    const origin = recorder();

    const res = await dispatchBlog(blogDeps(origin));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.purposes()).toEqual(["prefetch"]);
  });

  // allowHeader lets `purpose` through the filter, so a client's own value is
  // in the header set the forward is built from. Ours is the only one Next may
  // read: a visitor must not be able to make their request revalidate, nor to
  // opt out of the suppression by sending something else.
  it("overwrites whatever purpose the client sent", async () => {
    const origin = recorder();
    const request = new Request("https://app.example/blog", {
      headers: { purpose: "sniff" },
    });

    await dispatchBlog(blogDeps(origin), request);

    expect(origin.purposes()).toEqual(["prefetch"]);
    // The inbound request is untouched — this is the edge->Lambda leg only.
    expect(request.headers.get(PREFETCH)).toBe("sniff");
  });

  // The blocking revalidation already carries x-prerender-revalidate, whose
  // guard in Next's response cache sits ABOVE the prefetch one. The header
  // there would be dead weight that reads as though this leg were suppressed
  // too — which is the one thing that must never be true of it.
  it("never stamps the blocking revalidation forward", async () => {
    const pending: Promise<unknown>[] = [];
    const origin = recorder();

    const res = await dispatchBlog(
      blogDeps(origin, {
        entry: storedEntry(1_000),
        // 61s past a 60s window: stale, not expired.
        now: 1_000 + 61_000,
        waitUntil: (p) => pending.push(p),
      }),
    );
    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    await Promise.all(pending);

    expect(origin.requests).toHaveLength(1);
    expect(origin.requests[0].headers.get("x-prerender-revalidate")).toBe("TOKEN");
    expect(origin.purposes()).toEqual([null]);
  });

  // allowHeader lets `purpose` through, so a client value reaches the header set
  // the blocking leg is built from too. It matters when bypassToken is absent:
  // x-prerender-revalidate is then the empty string, which is not on-demand, and
  // Next's prefetch guard would suppress the very render this leg exists to
  // force — a refresh the admission already spent its sentinel on.
  it("never inherits a client-sent purpose on the blocking revalidation forward", async () => {
    const pending: Promise<unknown>[] = [];
    const origin = recorder();

    await dispatchBlog(
      blogDeps(origin, {
        entry: storedEntry(1_000),
        now: 1_000 + 61_000,
        waitUntil: (p) => pending.push(p),
      }),
      new Request("https://app.example/blog", {
        headers: { purpose: "prefetch" },
      }),
    );
    await Promise.all(pending);

    expect(origin.requests).toHaveLength(1);
    expect(origin.purposes()).toEqual([null]);
  });

  // BYPASS traffic is not ISR-governed and must stay byte-identical.
  it.each([
    [
      "a draft-mode cookie",
      new Request("https://app.example/blog", {
        headers: { cookie: "__prerender_bypass=1" },
      }),
    ],
    [
      "a bypass cookie the route names",
      new Request("https://app.example/blog", {
        headers: { cookie: "preview=1" },
      }),
    ],
    [
      "a non-GET method",
      new Request("https://app.example/blog", { method: "POST" }),
    ],
  ])("never stamps a bypass forward: %s", async (_name, request) => {
    const origin = recorder();

    const res = await dispatchBlog(blogDeps(origin), request);

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.purposes()).toEqual([null]);
  });

  it("never stamps a forward carrying a middleware set-cookie", async () => {
    const origin = recorder();

    const res = await dispatchResult(
      {
        resolvedPathname: "/blog",
        invocationTarget: { pathname: "/blog" },
        middleware: {
          headers: new Headers({ "x-middleware-set-cookie": "session=1" }),
          response: new Response(null),
          result: {},
        },
      },
      new Request("https://app.example/blog"),
      blogDeps(origin),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.purposes()).toEqual([null]);
  });

  // The stamp asks the Lambda not to start a render, so it may only go out
  // where something else can: the interception read is what judges this entry
  // stale and the admission tiers are what refresh it. Each of the three ways
  // that tier can be absent gets its own test, because none of them fails
  // loudly — the route just stops revalidating until hard expiry.
  it("never stamps when no colo cache is bound, which leaves no tier to refresh", async () => {
    const origin = recorder();

    await dispatchBlog(blogDeps(origin, { cache: undefined }));

    expect(origin.purposes()).toEqual([null]);
  });

  // The ISR store binding is optional by design (cloud/edge/cloudflare: a
  // substrate bootstrapped before there was a cache bucket carries no such
  // value, and the worker still uploads without it), so this is the deploy
  // shape the suppression is most likely to meet in production.
  it("never stamps when no ISR store is bound", async () => {
    const origin = recorder();

    await dispatchBlog(blogDeps(origin, { interception: undefined }));

    expect(origin.purposes()).toEqual([null]);
  });

  // A pages-router data request resolves to the same prerender target as its
  // html route, but interception reconstructs only html/RSC — so it takes the
  // interception-less path even on a fully bound worker.
  it("never stamps a pages-router data request", async () => {
    const origin = recorder();

    await dispatchBlog(
      blogDeps(origin),
      new Request("https://app.example/_next/data/t/blog.json"),
    );

    expect(origin.purposes()).toEqual([null]);
  });

  // Only a prerender is ISR-governed: a plain lambda route renders per request
  // and has no entry for Next to serve stale in the first place.
  it("never stamps a non-prerender route's forward", async () => {
    const origin = recorder();

    await dispatchResult(
      { resolvedPathname: "/api/hook", invocationTarget: { pathname: "/api/hook" } },
      new Request("https://app.example/api/hook"),
      {
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/api/hook": { kind: "lambda", id: "/api/hook" } },
        },
        functionUrls: { "/api/hook": "https://fn.example.com" },
        slug: "p1",
        app: "web",
        assetStore: noAssets(),
        fetch: origin.fetch,
      },
    );

    expect(origin.purposes()).toEqual([null]);
  });
});
