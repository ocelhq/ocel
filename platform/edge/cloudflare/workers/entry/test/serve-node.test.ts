import {
  compileOcelRouteTable,
  type OcelRouteTable,
  readOcelRouteTable,
} from "@platform/edge-contract/ocel-route-table";
import { describe, expect, it } from "vitest";
import { type ResolveBase, resolveServe, type ServeFetch } from "../src/index";
import {
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  SERVE_LOOKUP_ONLY_HEADER,
  SERVE_MISS_HEADER,
  SERVE_REFUSED_HEADER,
} from "../src/serve-key";
import { serveNodeOrigin } from "../src/serve-node";
import type { ServingPorts } from "../src/serving";
import { answerEveryRecordWith, capturing, FN_URL, makeRecord } from "./origin-deps";
import { objectStoreHolding, routeTableKey } from "./route-table-store";
import { fakeServing } from "./serving-deps";

const ORIGIN = "https://abc123.lambda-url.eu-west-2.on.aws/";

const token = "0123456789abcdef0123456789abcdef";

function newRouteTable(over: Partial<OcelRouteTable> = {}): OcelRouteTable {
  return {
    version: 1,
    basePath: "",
    trailingSlash: "ignore",
    headers: [],
    redirects: [],
    routes: [
      {
        src: "/blog/([^/]+)",
        function: "api",
        cache: { revalidate: 60, expire: 600, allowQuery: ["page"], bypassToken: token },
      },
      { src: "/", function: "api", cache: { revalidate: 30, bypassToken: token } },
      { src: "/feed", function: "api", cache: { revalidate: 10 } },
      { src: "/account", function: "api" },
    ],
    fallback: { function: "api" },
    ...over,
  };
}

function forwardTo(answer: () => Response = () => new Response("uncached")) {
  const forwarded: Request[] = [];
  return {
    forwarded,
    forward: async (request: Request) => {
      forwarded.push(request);
      return answer();
    },
  };
}

function setup(
  answer?: Parameters<typeof fakeServing>[0],
  uncached?: () => Response,
  routes: OcelRouteTable | null = newRouteTable(),
) {
  const fake = fakeServing(answer);
  const origin = forwardTo(uncached);
  const serve = serveNodeOrigin({
    serving: fake.serving,
    app: "web",
    origin: ORIGIN,
    routes: routes ? compileOcelRouteTable(readOcelRouteTable(routes)) : undefined,
    forward: origin.forward,
  });
  return { ...fake, ...origin, serve };
}

describe("a request a cached route names", () => {
  it("is answered by Serve under the app's own function URL, never by a direct fetch", async () => {
    const { serve, calls, forwarded } = setup();

    const res = await serve(new Request("https://shop.example.com/blog/a"));

    expect(await res.text()).toBe("page");
    expect(forwarded).toHaveLength(0);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/blog/a");
    expect(calls[0]!.coordinates).toEqual({
      host: "shop.example.com",
      app: "web",
      release: "r0a1b2c3d",
      variant: "",
    });
    expect(calls[0]!.props).toEqual({ kind: "origin" });
  });

  it("asks the origin to shape its response for the edge and names no stored entry", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/blog/a"));

    const { request } = calls[0]!;
    expect(request.headers.get("x-ocel-edge-cache")).toBe("1");
    const release = JSON.parse(request.headers.get(RENDER_RELEASE_HEADER)!) as RenderRelease;
    expect(release).toEqual({
      slug: "p1",
      isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
      tags: [],
      nodeApp: true,
    });
  });

  it("keys the entry by the query parameters its route allows, in sorted order, and passes no other on", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/blog/a?utm=x&page=2"));

    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/blog/a?page=2");
  });

  it("keys the entry by every query parameter, sorted, when its route lists none to allow", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/feed?b=2&a=1"));

    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/feed?a=1&b=2");
  });

  it("keys the entry by a parameter named _rsc and passes it on, as it does any other", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/feed?_rsc=1&a=2"));

    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/feed?_rsc=1&a=2");
  });

  it("keys the entry by no query parameter when its route allows an empty list", async () => {
    const routes = newRouteTable({
      routes: [{ src: "/feed", function: "api", cache: { revalidate: 10, allowQuery: [] } }],
    });
    const { serve, calls } = setup(undefined, undefined, routes);

    await serve(new Request("https://shop.example.com/feed?b=2&a=1"));

    expect(calls[0]!.originUrl).toBe("https://abc123.lambda-url.eu-west-2.on.aws/feed");
  });

  it("is answered from the shared entry when the visitor carries a cookie, which never reaches Serve", async () => {
    const { serve, calls, forwarded } = setup();

    await serve(
      new Request("https://shop.example.com/blog/a", { headers: { cookie: "session=1" } }),
    );

    expect(forwarded).toHaveLength(0);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.request.headers.has("cookie")).toBe(false);
  });

  it("tells the visitor how the cache answered, and hides what is for the edge alone", async () => {
    const { serve } = setup(
      () =>
        new Response("page", {
          headers: {
            "cf-cache-status": "HIT",
            "cache-tag": "r0a1b2c3d|posts",
            "cloudflare-cdn-cache-control": "max-age=60",
            "x-ocel-vary": "accept-encoding",
          },
        }),
    );

    const res = await serve(new Request("https://shop.example.com/blog/a"));

    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(res.headers.get("vary")).toBe("accept-encoding");
    for (const name of ["cache-tag", "cloudflare-cdn-cache-control", "cf-cache-status"]) {
      expect(res.headers.has(name), name).toBe(false);
    }
  });

  it("reports a miss when Serve filled the entry from the origin", async () => {
    const { serve } = setup();

    const res = await serve(new Request("https://shop.example.com/"));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("is rendered at the origin uncached when Serve refuses the origin", async () => {
    const { serve, forwarded } = setup(
      () => new Response("no", { status: 503, headers: { [SERVE_REFUSED_HEADER]: "1" } }),
    );

    const res = await serve(new Request("https://shop.example.com/blog/a"));

    expect(await res.text()).toBe("uncached");
    expect(forwarded).toHaveLength(1);
  });

  it("is looked up under its path inside the base path", async () => {
    const routes = newRouteTable({ basePath: "/docs" });
    const { serve, calls } = setup(undefined, undefined, routes);

    await serve(new Request("https://shop.example.com/docs/blog/a?page=2&utm=x"));
    await serve(new Request("https://shop.example.com/blog/a?page=2&utm=x"));

    expect(
      calls.map(({ originUrl }) => new URL(originUrl).pathname + new URL(originUrl).search),
    ).toEqual(["/docs/blog/a?page=2", "/blog/a?page=2&utm=x"]);
  });
});

describe("a request no cached route lets the edge cache", () => {
  const uncached = [
    ["a write", "https://shop.example.com/blog/a", { method: "POST", body: "x" }],
    ["a HEAD", "https://shop.example.com/blog/a", { method: "HEAD" }],
    [
      "a request with credentials",
      "https://shop.example.com/blog/a",
      { headers: { authorization: "Bearer t" } },
    ],
    ["a path with repeated slashes", "https://shop.example.com/blog//a", {}],
    [
      "a request with a bypass cookie on a route that has no cache rule",
      "https://shop.example.com/account",
      { headers: { cookie: "__prerender_bypass=any" } },
    ],
    [
      "a write to a route that has no cache rule",
      "https://shop.example.com/account",
      { method: "POST", body: "x" },
    ],
    [
      "a request with credentials to a path no route names",
      "https://shop.example.com/about",
      { headers: { authorization: "Bearer t" } },
    ],
  ] as const;

  for (const [name, url, init] of uncached) {
    it(`goes to the origin uncached for ${name}`, async () => {
      const { serve, calls, forwarded } = setup();

      await serve(new Request(url, init));

      expect(calls).toHaveLength(0);
      expect(forwarded).toHaveLength(1);
    });
  }

  it("goes to the origin uncached when the build states no route table", async () => {
    const { serve, calls, forwarded } = setup(undefined, undefined, null);

    await serve(new Request("https://shop.example.com/blog/a"));

    expect(calls).toHaveLength(0);
    expect(forwarded).toHaveLength(1);
  });
});

describe("a request on a route no cache rule governs", () => {
  const unruled = [
    ["a route with no cache rule", "https://shop.example.com/account"],
    ["a path no route names", "https://shop.example.com/about"],
  ] as const;

  for (const [name, url] of unruled) {
    it(`is answered by Serve for ${name}, keyed by every query parameter in sorted order`, async () => {
      const { serve, calls, forwarded } = setup();

      const res = await serve(new Request(`${url}?b=2&a=1`));

      expect(await res.text()).toBe("page");
      expect(forwarded).toHaveLength(0);
      expect(calls).toHaveLength(1);
      expect(calls[0]!.originUrl).toBe(`${ORIGIN}${new URL(url).pathname.slice(1)}?a=1&b=2`);
      expect(calls[0]!.props).toEqual({ kind: "origin" });
    });
  }

  it("asks the origin to shape its response for the edge, and tells Serve it is a node app's", async () => {
    const { serve, calls } = setup();

    await serve(new Request("https://shop.example.com/account"));

    const { request } = calls[0]!;
    expect(request.headers.get("x-ocel-edge-cache")).toBe("1");
    expect(request.headers.has(SERVE_LOOKUP_ONLY_HEADER)).toBe(false);
    expect(JSON.parse(request.headers.get(RENDER_RELEASE_HEADER)!)).toEqual({
      slug: "p1",
      isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
      tags: [],
      nodeApp: true,
    });
  });

  it("reaches the origin uncached when Serve refuses the origin", async () => {
    const { serve, forwarded } = setup(
      () => new Response("no", { status: 503, headers: { [SERVE_REFUSED_HEADER]: "1" } }),
    );

    const res = await serve(new Request("https://shop.example.com/account"));

    expect(await res.text()).toBe("uncached");
    expect(forwarded).toHaveLength(1);
  });

  it("only looks for a stored entry when the visitor carries a cookie, and passes the cookie to nobody but the origin", async () => {
    const { serve, calls, forwarded } = setup();

    await serve(new Request("https://shop.example.com/account", { headers: { cookie: "s=1" } }));

    expect(calls).toHaveLength(1);
    expect(calls[0]!.request.headers.get(SERVE_LOOKUP_ONLY_HEADER)).toBe("1");
    expect(calls[0]!.request.headers.has("cookie")).toBe(false);
    expect(forwarded).toHaveLength(0);
  });

  it("answers a visitor with a cookie from the stored entry when there is one", async () => {
    const { serve, forwarded } = setup(
      () => new Response("stored page", { headers: { "cf-cache-status": "HIT" } }),
    );

    const res = await serve(
      new Request("https://shop.example.com/account", { headers: { cookie: "s=1" } }),
    );

    expect(await res.text()).toBe("stored page");
    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(forwarded).toHaveLength(0);
  });

  it("sends a visitor with a cookie to the origin with it, uncached, when there is no entry", async () => {
    const { serve, forwarded } = setup(
      () => new Response(null, { status: 404, headers: { [SERVE_MISS_HEADER]: "1" } }),
    );

    const res = await serve(
      new Request("https://shop.example.com/account", { headers: { cookie: "s=1" } }),
    );

    expect(await res.text()).toBe("uncached");
    expect(forwarded).toHaveLength(1);
    expect(forwarded[0]!.headers.get("cookie")).toBe("s=1");
    expect(forwarded[0]!.headers.has("x-ocel-edge-cache")).toBe(false);
  });

  it("sends a visitor with a cookie to the origin when Serve refuses the origin", async () => {
    const { serve, forwarded } = setup(
      () => new Response("no", { status: 503, headers: { [SERVE_REFUSED_HEADER]: "1" } }),
    );

    await serve(new Request("https://shop.example.com/account", { headers: { cookie: "s=1" } }));

    expect(forwarded).toHaveLength(1);
  });

  it("is looked up under the query a cookie-carrying visitor sent, in sorted order", async () => {
    const { serve, calls } = setup();

    await serve(
      new Request("https://shop.example.com/account?b=2&a=1", { headers: { cookie: "s=1" } }),
    );

    expect(calls[0]!.originUrl).toBe(`${ORIGIN}account?a=1&b=2`);
  });
});

describe("a request bypassing the cache", () => {
  it("goes to the origin with its cookie, never touching Serve", async () => {
    const { serve, calls, forwarded } = setup();

    await serve(
      new Request("https://shop.example.com/blog/a", {
        headers: { cookie: `a=b; __prerender_bypass=${token}` },
      }),
    );

    expect(calls).toHaveLength(0);
    expect(forwarded).toHaveLength(1);
    expect(forwarded[0]!.headers.get("cookie")).toBe(`a=b; __prerender_bypass=${token}`);
  });

  it("is a request for the cache when its cookie holds another token", async () => {
    const { serve, calls } = setup();

    await serve(
      new Request("https://shop.example.com/blog/a", {
        headers: { cookie: "__prerender_bypass=wrong" },
      }),
    );

    expect(calls).toHaveLength(1);
  });

  it("is a request for the cache on a route that states no token", async () => {
    const { serve, calls } = setup();

    await serve(
      new Request("https://shop.example.com/feed", {
        headers: { cookie: `__prerender_bypass=${token}` },
      }),
    );

    expect(calls).toHaveLength(1);
  });
});

describe("a request carrying the revalidate token", () => {
  const revalidating = { headers: { "x-prerender-revalidate": token } };

  it("is rendered fresh at the origin, then clears the path's entry before it is answered", async () => {
    const { serve, calls, forwarded, purges } = setup();

    const res = await serve(new Request("https://shop.example.com/blog/a", revalidating));

    expect(await res.text()).toBe("uncached");
    expect(calls).toHaveLength(0);
    expect(forwarded).toHaveLength(1);
    expect(forwarded[0]!.headers.get("x-prerender-revalidate")).toBe(token);
    expect(purges).toEqual([
      { groups: [{ mode: "purge", tags: ["r0a1b2c3d|path:/blog/a"] }], awaited: true },
    ]);
  });

  it("clears nothing when the origin could not render", async () => {
    const { serve, purges } = setup(undefined, () => new Response("broken", { status: 500 }));

    const res = await serve(new Request("https://shop.example.com/blog/a", revalidating));

    expect(res.status).toBe(500);
    expect(purges).toEqual([]);
  });

  it("is a plain request for the cache when its token is wrong, or it is a write", async () => {
    const { serve, calls, purges } = setup();

    await serve(
      new Request("https://shop.example.com/blog/a", {
        headers: { "x-prerender-revalidate": "wrong" },
      }),
    );
    await serve(
      new Request("https://shop.example.com/blog/b", {
        method: "POST",
        body: "x",
        ...revalidating,
      }),
    );

    expect(calls).toHaveLength(1);
    expect(purges).toEqual([]);
  });
});

describe("a redirect the route table states", () => {
  const redirecting = newRouteTable({
    trailingSlash: "never",
    redirects: [{ src: "/old/(.*)", location: "/new/$1", status: 308 }],
    headers: [{ src: "/(.*)", headers: { "x-frame-options": "DENY" } }],
  });

  it("carries the query over before the fragment its location states", async () => {
    const { serve } = setup(
      undefined,
      undefined,
      newRouteTable({ redirects: [{ src: "/old", location: "/new#section", status: 307 }] }),
    );

    const res = await serve(new Request("https://shop.example.com/old?x=1"));

    expect(res.headers.get("location")).toBe("/new?x=1#section");
  });

  it("is answered by the gateway itself, with the header rules, and the query carried over", async () => {
    const { serve, calls, forwarded } = setup(undefined, undefined, redirecting);

    const res = await serve(new Request("https://shop.example.com/old/a?x=1"));

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/new/a?x=1");
    expect(res.headers.get("x-frame-options")).toBe("DENY");
    expect(calls).toHaveLength(0);
    expect(forwarded).toHaveLength(0);
  });

  it("includes the trailing slash its mode removes from a read, and not from a write", async () => {
    const { serve, forwarded } = setup(undefined, undefined, redirecting);

    const read = await serve(new Request("https://shop.example.com/blog/a/"));
    const write = await serve(
      new Request("https://shop.example.com/blog/a/", { method: "POST", body: "x" }),
    );

    expect(read.status).toBe(308);
    expect(read.headers.get("location")).toBe("/blog/a");
    expect(write.status).toBe(200);
    expect(forwarded).toHaveLength(1);
  });

  it("sends a path that begins with two slashes to a location on its own site", async () => {
    const { serve } = setup(
      undefined,
      undefined,
      newRouteTable({
        trailingSlash: "never",
        redirects: [{ src: "/go/(.*)", location: "/$1", status: 307 }],
      }),
    );

    const slash = await serve(new Request("https://shop.example.com//evil.com/"));
    const substituted = await serve(new Request("https://shop.example.com/go//evil.com"));

    expect(slash.headers.get("location")).toBe("/evil.com");
    expect(substituted.headers.get("location")).toBe("/evil.com");
  });
});

describe("a purge the origin announces on a response Serve did not cache", () => {
  const announcing = () =>
    new Response("done", {
      headers: { "x-ocel-purge": "purge r0a1b2c3d|posts invalidate r0a1b2c3d|feed" },
    });

  it("is carried out before a write is answered, and hidden from the visitor", async () => {
    const { serve, purges } = setup(undefined, announcing);

    const res = await serve(
      new Request("https://shop.example.com/api/publish", { method: "POST", body: "x" }),
    );

    expect(purges).toEqual([
      {
        groups: [
          { mode: "purge", tags: ["r0a1b2c3d|posts"] },
          { mode: "invalidate", tags: ["r0a1b2c3d|feed"] },
        ],
        awaited: true,
      },
    ]);
    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(await res.text()).toBe("done");
  });

  it("is carried out without holding a read back", async () => {
    const { serve, purges } = setup(undefined, announcing);

    await serve(
      new Request("https://shop.example.com/about", {
        headers: { cookie: "__prerender_bypass=any" },
      }),
    );

    expect(purges.map(({ awaited }) => awaited)).toEqual([false]);
  });
});

describe("a node release resolved by the gateway", () => {
  const ports = (served: Request[]): ServingPorts => ({
    serve: () => ({
      fetch: async (request) => {
        served.push(request);
        return new Response("cached page", { headers: { "cf-cache-status": "HIT" } });
      },
      purge: async () => ({ ok: true }),
    }),
    queue: { raise: async () => {} },
    times: { record: async () => {} },
    waitUntil: () => {},
  });

  async function resolved(
    over: Parameters<typeof makeRecord>[0],
    routes: OcelRouteTable | string | undefined,
    serving?: ServingPorts,
  ) {
    const record = makeRecord({ isrPrefix: "prod/p1/api/r0a1b2c3d/isr", ...over });
    const wire = capturing();
    const base: ResolveBase = {
      assetStore: {
        cache: { match: async () => undefined, put: async () => {} },
        waitUntil: () => {},
      },
      originFetch: wire.fetch,
      routeTableStore:
        routes === undefined
          ? undefined
          : objectStoreHolding({ [routeTableKey()]: routes as never }),
      serving,
    };
    const serve = await resolveServe(
      {
        binding: answerEveryRecordWith(async () => ({
          kind: "record",
          release: "r0a1b2c3d",
          record,
        })),
        slug: "p1",
        host: "api.example.com",
        app: record.app,
      },
      base,
    );
    return { serve, wire };
  }

  const located = { routeTable: { format: "ocel" as const, key: routeTableKey() } };

  it("answers a route the stored table caches from Serve, and reaches the origin for a write", async () => {
    const served: Request[] = [];
    const { serve, wire } = await resolved(located, newRouteTable(), ports(served));

    const cached = await (serve as ServeFetch)(new Request("https://api.example.com/blog/a"));
    const direct = await (serve as ServeFetch)(
      new Request("https://api.example.com/about", { method: "POST", body: "x" }),
    );

    expect(await cached.text()).toBe("cached page");
    expect(served).toHaveLength(1);
    expect(await direct.text()).toBe("origin");
    expect(wire.calls.map(({ url }) => url)).toEqual([`${FN_URL}about`]);
  });

  it("reaches the origin for every path when the release has no route table", async () => {
    const served: Request[] = [];
    const { serve, wire } = await resolved({}, undefined, ports(served));

    await (serve as ServeFetch)(new Request("https://api.example.com/blog/a"));

    expect(served).toHaveLength(0);
    expect(wire.calls).toHaveLength(1);
  });

  it("answers 503 when the stored table cannot be read or is not a valid table", async () => {
    for (const stored of ["{", undefined]) {
      const { serve } = await resolved(located, stored, ports([]));

      const res = serve as Response;
      expect(res.status, String(stored)).toBe(503);
    }
  });
});
