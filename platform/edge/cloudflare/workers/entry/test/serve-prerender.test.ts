import { baseDeps } from "@framework/next-router/test-support/dispatch-scenario";
import { describe, expect, it } from "vitest";
import { dispatchResult, type RouteDeps } from "../src/index";
import { buildEdgeUrl, EDGE_RELEASE_HEADER, readEdgeEntry } from "../src/serve-key";
import { type FakeServing, fakeServing } from "./serving-deps";

type Target = Record<string, unknown>;

const actionTarget: Target = {
  config: { allowHeader: ["host"], bypassFor: [{ type: "header", key: "next-action" }] },
};

function recorder(answer: () => Response = () => new Response("from-origin")) {
  const requests: Request[] = [];
  const fetch = (async (request: Request) => {
    requests.push(request);
    return answer();
  }) as unknown as typeof fetch;
  return { requests, fetch };
}

function deps(
  served: FakeServing,
  over: { target?: Target; origin?: ReturnType<typeof recorder> } = {},
): RouteDeps {
  const origin = over.origin ?? recorder();
  return baseDeps({
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {
        "/blog": {
          kind: "prerender",
          id: "/blog",
          config: { allowHeader: ["host"], bypassToken: "TOKEN" },
          allowQuery: ["page"],
          ...over.target,
        },
      },
    },
    functionUrls: { "/blog": "https://fn.example.com" },
    fetch: origin.fetch,
    serving: served.serving,
  });
}

const get = (path: string, deps: RouteDeps, init?: RequestInit) =>
  dispatchResult(
    { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
    new Request(`https://Shop.Example.com${path}`, init),
    deps,
  );

function cached(status: string, extra: Record<string, string> = {}) {
  return () =>
    new Response("page", {
      headers: {
        "cf-cache-status": status,
        "cache-tag": "r0a1b2c3d,r0a1b2c3d|path:/blog",
        "cloudflare-cdn-cache-control": "max-age=60",
        "cache-control": "public, max-age=0, must-revalidate",
        ...extra,
      },
    });
}

describe("a prerender served through Serve", () => {
  it("fetches the origin URL through Serve keyed by the public host, the app and the release token", async () => {
    const served = fakeServing();

    await get("/blog", deps(served));

    expect(served.calls).toHaveLength(1);
    expect(served.calls[0].props).toEqual({ kind: "origin" });
    expect(served.calls[0].coordinates).toEqual({
      host: "shop.example.com",
      app: "web",
      release: "r0a1b2c3d",
      variant: "",
    });
    expect(served.calls[0].originUrl).toBe("https://fn.example.com/blog");
  });

  it("tells the origin the request is for the edge cache, so it shapes the response", async () => {
    const served = fakeServing();

    await get("/blog", deps(served));

    expect(served.calls[0].request.headers.get("x-ocel-edge-cache")).toBe("1");
  });

  it("keys a visitor's own x-ocel-edge-cache out of what the origin is sent on a bypass", async () => {
    const origin = recorder();

    await get("/blog", deps(fakeServing(), { origin, target: actionTarget }), {
      method: "POST",
      body: "x",
      headers: { "next-action": "1", "x-ocel-edge-cache": "1" },
    });

    expect(origin.requests[0].headers.has("x-ocel-edge-cache")).toBe(false);
  });

  it("hands Serve neither the visitor's cookie nor their authorization", async () => {
    const served = fakeServing();

    await get("/blog", deps(served), {
      headers: { cookie: "session=1", authorization: "Bearer t" },
    });

    const sent = served.calls[0].request.headers;
    expect(sent.has("cookie")).toBe(false);
    expect(sent.has("authorization")).toBe(false);
  });

  it("keys two requests that differ only in a query the route ignores to one URL", async () => {
    const served = fakeServing();

    await get("/blog?ref=a&page=2", deps(served));
    await get("/blog?page=2&utm=b", deps(served));

    expect(served.calls.map(({ originUrl }) => originUrl)).toEqual([
      "https://fn.example.com/blog?page=2",
      "https://fn.example.com/blog?page=2",
    ]);
    expect(served.calls[0].request.url).toBe(served.calls[1].request.url);
  });

  it("keys an RSC request apart from the document by variant", async () => {
    const served = fakeServing();

    await get("/blog", deps(served));
    await get("/blog", deps(served), { headers: { rsc: "1" } });

    expect(served.calls.map(({ coordinates }) => coordinates.variant)).toEqual(["", ".rsc"]);
    expect(served.calls[0].request.url).not.toBe(served.calls[1].request.url);
    expect(served.calls[0].props).toEqual(served.calls[1].props);
  });

  it.each([
    ["HIT", "HIT"],
    ["REVALIDATED", "HIT"],
    ["STALE", "STALE"],
    ["UPDATING", "STALE"],
    ["MISS", "MISS"],
    ["EXPIRED", "MISS"],
    ["DYNAMIC", "MISS"],
  ])("reports Workers Cache status %s as the %s tier", async (status, tier) => {
    const res = await get("/blog", deps(fakeServing(cached(status))));

    expect(res.headers.get("x-ocel-cache")).toBe(tier);
  });

  it("reports a Serve response with no cache status as a miss", async () => {
    const res = await get("/blog", deps(fakeServing(() => new Response("page"))));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("keeps the cache's own headers off the visitor's response", async () => {
    const res = await get("/blog", deps(fakeServing(cached("HIT"))));

    expect(res.headers.has("cache-tag")).toBe(false);
    expect(res.headers.has("cloudflare-cdn-cache-control")).toBe(false);
    expect(res.headers.has("cf-cache-status")).toBe(false);
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });

  it("gives the visitor back the Vary the origin sent, which Serve moved to x-ocel-vary", async () => {
    const res = await get(
      "/blog",
      deps(fakeServing(cached("HIT", { "x-ocel-vary": "rsc, next-router-state-tree" }))),
    );

    expect(res.headers.get("vary")).toBe("rsc, next-router-state-tree");
    expect(res.headers.has("x-ocel-vary")).toBe(false);
  });

  it("reports a stored page's x-nextjs-cache as the tier that served it", async () => {
    const res = await get("/blog", deps(fakeServing(cached("HIT", { "x-nextjs-cache": "MISS" }))));

    expect(res.headers.get("x-nextjs-cache")).toBe("HIT");
  });

  it("serves a HEAD request from the origin, never from Serve", async () => {
    const served = fakeServing();
    const origin = recorder();

    await get("/blog", deps(served, { origin }), { method: "HEAD" });

    expect(served.calls).toHaveLength(0);
    expect(origin.requests).toHaveLength(1);
  });
});

describe("a prerender that must not be stored", () => {
  async function throughOrigin(
    path: string,
    init: RequestInit | undefined,
    target?: Target,
  ): Promise<{ res: Response; served: FakeServing; origin: ReturnType<typeof recorder> }> {
    const served = fakeServing();
    const origin = recorder();
    const res = await get(path, deps(served, { origin, target }), init);
    expect(served.calls).toHaveLength(0);
    return { res, served, origin };
  }

  it("goes to the origin for a draft-mode visitor, with their cookie", async () => {
    const { res, origin } = await throughOrigin("/blog", {
      headers: { cookie: "__prerender_bypass=abc" },
    });

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.requests[0].headers.get("cookie")).toBe("__prerender_bypass=abc");
  });

  it("goes to the origin for a POST", async () => {
    const { res } = await throughOrigin(
      "/blog",
      { method: "POST", body: "x", headers: { "next-action": "1" } },
      actionTarget,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("goes to the origin for a runtime prefetch, which no cache may answer", async () => {
    const { res } = await throughOrigin("/blog", {
      headers: { rsc: "1", "next-router-prefetch": "2" },
    });

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("goes to the origin for the document of a PPR route, whose resume cannot be stored", async () => {
    const { res } = await throughOrigin("/blog", undefined, {
      config: { renderingMode: "PARTIALLY_STATIC" },
    });

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("goes to the origin for bare RSC of a PPR route", async () => {
    const { res } = await throughOrigin(
      "/blog",
      { headers: { rsc: "1" } },
      {
        config: { renderingMode: "PARTIALLY_STATIC" },
      },
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });
});

describe("an edge-runtime prerender", () => {
  const edgeTarget: Target = { edgeEntryKey: "middleware_app/edge-blog", tags: ["posts"] };

  it("is rendered by Serve and stored under the purge scope, keyed by its edge entry, host and release", async () => {
    const served = fakeServing();
    const origin = recorder();

    await get("/blog?page=2&utm=x", deps(served, { origin, target: edgeTarget }));

    expect(origin.requests).toHaveLength(0);
    expect(served.calls).toHaveLength(1);
    const [call] = served.calls;
    expect(call.props).toEqual({ kind: "origin" });
    expect(call.coordinates).toEqual({
      host: "shop.example.com",
      app: "web",
      release: "r0a1b2c3d",
      variant: "",
    });
    expect(readEdgeEntry(call.request.url)).toBe("middleware_app/edge-blog");
    expect(buildEdgeUrl(call.request.url)).toBe("https://shop.example.com/blog?page=2");
  });

  it("tells Serve the release to render it from and the route's tags", async () => {
    const served = fakeServing();

    await get("/blog", deps(served, { target: edgeTarget }));

    expect(JSON.parse(served.calls[0].request.headers.get(EDGE_RELEASE_HEADER) ?? "null")).toEqual({
      slug: "p1",
      isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
      tags: ["posts"],
    });
  });

  it("keys its RSC variant apart from its document", async () => {
    const served = fakeServing();

    await get("/blog", deps(served, { target: edgeTarget }));
    await get("/blog", deps(served, { target: edgeTarget }), { headers: { rsc: "1" } });

    expect(served.calls.map(({ coordinates }) => coordinates.variant)).toEqual(["", ".rsc"]);
    expect(served.calls[0].request.url).not.toBe(served.calls[1].request.url);
  });
});

describe("a prerender with no Serve to go through", () => {
  it("renders at the origin and stores nothing", async () => {
    const origin = recorder();
    const routeDeps = { ...deps(fakeServing(), { origin }), serving: undefined };

    const res = await get("/blog", routeDeps);

    expect(await res.text()).toBe("from-origin");
    expect(origin.requests).toHaveLength(1);
  });
});

describe("an on-demand revalidation with the bypass token", () => {
  const onDemand = { headers: { "x-prerender-revalidate": "TOKEN" } };

  it("renders at the origin with the token, past the cache", async () => {
    const served = fakeServing();
    const origin = recorder();

    const res = await get("/blog", deps(served, { origin }), onDemand);

    expect(served.calls).toHaveLength(0);
    expect(origin.requests[0].headers.get("x-prerender-revalidate")).toBe("TOKEN");
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("then purges the path's tag in the release, and waits for it", async () => {
    const served = fakeServing();

    await get("/blog", deps(served), onDemand);

    expect(served.purges).toEqual([
      { groups: [{ mode: "purge", tags: ["r0a1b2c3d|path:/blog"] }], awaited: true },
    ]);
  });

  it("purges nothing when the render failed", async () => {
    const served = fakeServing();
    const origin = recorder(() => new Response("boom", { status: 500 }));

    await get("/blog", deps(served, { origin }), onDemand);

    expect(served.purges).toEqual([]);
  });

  it("does not run for a visitor who sent no token", async () => {
    const served = fakeServing();

    await get("/blog", deps(served), { headers: { "x-prerender-revalidate": "WRONG" } });

    expect(served.purges).toEqual([]);
  });
});

describe("an origin response announcing a purge", () => {
  function acting(headers: Record<string, string>, served: FakeServing) {
    const origin = recorder(
      () =>
        new Response("done", {
          headers: { "x-ocel-purge": "invalidate r0a1b2c3d|a purge r0a1b2c3d|b", ...headers },
        }),
    );
    return deps(served, { origin, target: actionTarget });
  }

  const post = (routeDeps: RouteDeps) =>
    get("/blog", routeDeps, { method: "POST", body: "x", headers: { "next-action": "1" } });

  it("hands the gateway's serving the groups, unawaited for a route handler", async () => {
    const served = fakeServing();

    const res = await post(acting({}, served));

    expect(served.purges).toEqual([
      {
        groups: [
          { mode: "invalidate", tags: ["r0a1b2c3d|a"] },
          { mode: "purge", tags: ["r0a1b2c3d|b"] },
        ],
        awaited: false,
      },
    ]);
    expect(res.headers.has("x-ocel-purge")).toBe(false);
  });

  it("has the gateway await the purge for a Server Action that revalidated", async () => {
    const served = fakeServing();

    await post(acting({ "x-action-revalidated": "[[],0,0]" }, served));

    expect(served.purges.map(({ awaited }) => awaited)).toEqual([true]);
  });
});
