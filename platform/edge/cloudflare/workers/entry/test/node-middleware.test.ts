import { describe, expect, it, vi } from "vitest";

import { serve, type RouteDeps } from "../src/index";
import type { AssetBucket } from "../src/assets";

const ENTRY_HEADER = "x-ocel-entry";
const MW_ID = "middleware-bundle";
const MW_URL = "https://mw.example.com";

const emptyRoutes = {
  beforeMiddleware: [],
  beforeFiles: [],
  afterFiles: [],
  dynamicRoutes: [],
  onMatch: [],
  fallback: [],
};

function assetStoreServing(files: Record<string, string>): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      const body = files[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

function deps(overrides: Partial<RouteDeps> = {}): RouteDeps {
  return {
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: emptyRoutes,
      dispatch: {},
    },
    functionUrls: {},
    slug: "p1",
    app: "web",
    assetStore: assetStoreServing({}),
    ...overrides,
  } as RouteDeps;
}

function staticDeps(middleware: unknown, overrides: Partial<RouteDeps> = {}): RouteDeps {
  return deps({
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: ["/static.txt"],
      routes: emptyRoutes,
      dispatch: { "/static.txt": { kind: "static" } },
      middleware,
    },
    assetStore: assetStoreServing({ "/static.txt": "the-file" }),
    functionUrls: { [MW_ID]: MW_URL },
    ...overrides,
  } as Partial<RouteDeps>);
}

function nodeMiddleware(matchers?: unknown) {
  return {
    runtime: "nodejs" as const,
    id: MW_ID,
    entryKey: "/_middleware" as const,
    ...(matchers !== undefined && { matchers }),
  };
}

function fakeOrigin(handler: (request: Request) => Response | Promise<Response>) {
  const requests: Request[] = [];
  const fetch = (async (input: Request) => {
    requests.push(input);
    return handler(input);
  }) as unknown as typeof fetch;
  return { fetch, requests };
}

function serviceThrottle() {
  return new Response(null, {
    status: 429,
    headers: { "x-amzn-errortype": "TooManyRequestsException" },
  });
}

const passthroughBody =
  'async () => new Response(null, { headers: { "x-middleware-next": "1", "x-mw-ran": "1" } })';

describe("node middleware matchers", () => {
  it("does not invoke middleware for a path its matchers exclude", async () => {
    const origin = fakeOrigin(() => new Response(null, { status: 200 }));
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware([{ sourceRegex: "^/dashboard$" }]), { fetch: origin.fetch }),
    );

    expect(origin.requests.length).toBe(0);
    expect(await res.text()).toBe("the-file");
  });

  it("invokes middleware for a path its matchers name", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1", "x-mw-ran": "1" } }),
    );
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware([{ sourceRegex: "^/static\\.txt$" }]), { fetch: origin.fetch }),
    );

    expect(origin.requests.length).toBe(1);
    expect(res.headers.get("x-mw-ran")).toBe("1");
  });
});

describe("node middleware forwarding", () => {
  it("carries x-ocel-entry: /_middleware and reaches the bundle named by middleware.id", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1" } }),
    );
    await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(origin.requests.length).toBe(1);
    const forwarded = origin.requests[0];
    expect(new URL(forwarded.url).host).toBe(new URL(MW_URL).host);
    expect(forwarded.headers.get(ENTRY_HEADER)).toBe("/_middleware");
  });

  it("pins the forwarded URL and the x-forwarded-* pair the dispatcher rebuilds the public URL from", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1" } }),
    );
    await serve(
      new Request("https://app.example/static.txt?a=1"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    const forwarded = origin.requests[0];
    const url = new URL(forwarded.url);
    expect(url.pathname).toBe("/static.txt");
    expect(url.search).toBe("?a=1");
    expect(forwarded.headers.get("x-forwarded-host")).toBe("app.example");
    expect(forwarded.headers.get("x-forwarded-proto")).toBe("https");
  });

  it("responds directly with the middleware's own body and status", async () => {
    const origin = fakeOrigin(
      () =>
        new Response(JSON.stringify({ error: "nope" }), {
          status: 401,
          headers: { "content-type": "application/json" },
        }),
    );
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(401);
    expect(await res.json()).toEqual({ error: "nope" });
  });

  it("redirects with the middleware's Set-Cookie intact", async () => {
    const origin = fakeOrigin(
      () =>
        new Response(null, {
          status: 307,
          headers: { location: "/login", "set-cookie": "sid=abc; Path=/" },
        }),
    );
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/login");
    expect(res.headers.getSetCookie()).toEqual(["sid=abc; Path=/"]);
  });

  it("forwards a request-header override to the origin", async () => {
    const origin = fakeOrigin((req) => {
      if (new URL(req.url).host === new URL(MW_URL).host) {
        return new Response(null, {
          headers: {
            "x-middleware-next": "1",
            "x-middleware-override-headers": "x-user",
            "x-middleware-request-x-user": "alice",
          },
        });
      }
      return new Response(req.headers.get("x-user") ?? "none");
    });

    const res = await serve(
      new Request("https://app.example/api/me"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/api/me"],
          routes: emptyRoutes,
          dispatch: { "/api/me": { kind: "lambda", id: "/api/me" } },
          middleware: nodeMiddleware(),
        },
        functionUrls: { [MW_ID]: MW_URL, "/api/me": "https://fn.example.com" },
        fetch: origin.fetch,
      } as Partial<RouteDeps>),
    );

    expect(await res.text()).toBe("alice");
  });

  it("routes a rewrite destination on its routing form (trailing-slash canonicalisation)", async () => {
    const PAGE = "/page";
    const origin = fakeOrigin((req) => {
      const url = new URL(req.url);
      if (url.host === new URL(MW_URL).host) {
        return new Response(null, {
          headers: { "x-middleware-rewrite": "/b/" },
        });
      }
      return new Response(url.pathname);
    });

    const res = await serve(
      new Request(`https://app.example${PAGE}/`),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          trailingSlash: true,
          pathnames: [PAGE, "/b"],
          routes: emptyRoutes,
          dispatch: { [PAGE]: { kind: "static" }, "/b": { kind: "lambda", id: "/b" } },
          middleware: nodeMiddleware(),
        },
        assetStore: assetStoreServing({ [`${PAGE}.html`]: "the page", "/404.html": "nope" }),
        functionUrls: { [MW_ID]: MW_URL, "/b": "https://fn.example.com" },
        fetch: origin.fetch,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/b");
    expect(await res.text()).toBe("/b");
  });
});

describe("Next's internal protocol headers are not honoured from the client", () => {
  it("does not forward a client-supplied x-nextjs-data header to node middleware", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1" } }),
    );
    await serve(
      new Request("https://app.example/static.txt", {
        headers: { "x-nextjs-data": "1" },
      }),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(origin.requests[0].headers.has("x-nextjs-data")).toBe(false);
  });

  it("does not forward a client-supplied x-matched-path header to node middleware", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1" } }),
    );
    await serve(
      new Request("https://app.example/static.txt", {
        headers: { "x-matched-path": "/forged" },
      }),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(origin.requests[0].headers.has("x-matched-path")).toBe(false);
  });
});

describe("x-nextjs-data on the middleware invocation", () => {
  it("sets x-nextjs-data on a genuine data request's middleware invocation", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1" } }),
    );
    await serve(
      new Request("https://app.example/_next/data/t/static.txt.json"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(origin.requests[0].headers.get("x-nextjs-data")).toBe("1");
  });

  it("does not set x-nextjs-data on an ordinary document request", async () => {
    const origin = fakeOrigin(
      () => new Response(null, { headers: { "x-middleware-next": "1" } }),
    );
    await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(origin.requests[0].headers.has("x-nextjs-data")).toBe(false);
  });

  it("carries a data-request redirect through as x-nextjs-redirect with no Location", async () => {
    const origin = fakeOrigin((req) =>
      req.headers.get("x-nextjs-data") === "1"
        ? new Response(null, { status: 307, headers: { "x-nextjs-redirect": "/somewhere" } })
        : new Response(null, { status: 307, headers: { location: "/somewhere" } }),
    );
    const res = await serve(
      new Request("https://app.example/_next/data/t/static.txt.json", {
        redirect: "manual",
      }),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("x-nextjs-redirect")).toBe("/somewhere");
    expect(res.headers.has("location")).toBe(false);
  });
});

describe("node middleware fails closed", () => {
  it("returns 500 and never serves the page when no function URL is bound for the bundle", async () => {
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { functionUrls: {} }),
    );

    expect(res.status).toBe(500);
    expect(await res.text()).not.toContain("the-file");
  });

  it("still fails closed when the invoker throws a falsy value", async () => {
    vi.useFakeTimers();
    try {
      const origin = fakeOrigin(() => {
        throw undefined;
      });

      const resPromise = serve(
        new Request("https://app.example/static.txt"),
        staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
      );
      await vi.runAllTimersAsync();
      const res = await resPromise;

      expect(res.status).toBe(500);
      expect(await res.text()).not.toContain("the-file");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("node middleware retries a throttled origin", () => {
  it("serves the request when a service throttle is followed by success", async () => {
    let calls = 0;
    const origin = fakeOrigin(() => {
      calls++;
      if (calls === 1) return serviceThrottle();
      return new Response(null, { headers: { "x-middleware-next": "1", "x-mw-ran": "1" } });
    });

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("the-file");
    expect(res.headers.get("x-mw-ran")).toBe("1");
    expect(calls).toBe(2);
  });

  it("fails closed with a 500 once the retry budget on repeated service throttles is exhausted", async () => {
    vi.useFakeTimers();
    try {
      const origin = fakeOrigin(() => serviceThrottle());

      const resPromise = serve(
        new Request("https://app.example/static.txt"),
        staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
      );
      await vi.runAllTimersAsync();
      const res = await resPromise;

      expect(res.status).toBe(500);
      expect(await res.text()).not.toContain("the-file");
      expect(origin.requests.length).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not retry a real 500 the app produced on purpose", async () => {
    const origin = fakeOrigin(
      () => new Response("app error", { status: 500 }),
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(500);
    expect(await res.text()).toBe("app error");
    expect(origin.requests.length).toBe(1);
  });

  it("does not retry an app-authored 429 — it reaches the client verbatim, uncounted against the retry budget", async () => {
    const origin = fakeOrigin(
      () =>
        new Response("rate limited", {
          status: 429,
          headers: { "retry-after": "60" },
        }),
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(429);
    expect(res.headers.get("retry-after")).toBe("60");
    expect(await res.text()).toBe("rate limited");
    expect(origin.requests.length).toBe(1);
  });
});

describe("legacy edge middleware is unaffected", () => {
  function edgeStaticDeps(middleware: unknown, overrides: Partial<RouteDeps> = {}): RouteDeps {
    return deps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/static.txt"],
        routes: emptyRoutes,
        dispatch: { "/static.txt": { kind: "static" } },
        middleware,
      },
      assetStore: assetStoreServing({ "/static.txt": "the-file" }),
      ...overrides,
    } as Partial<RouteDeps>);
  }

  it("still calls deps.edge for an explicit runtime: 'edge' manifest", async () => {
    let calls = 0;
    const edge: RouteDeps["edge"] = async () => {
      calls++;
      return new Response(null, { headers: { "x-middleware-next": "1" } });
    };
    const res = await serve(
      new Request("https://app.example/static.txt"),
      edgeStaticDeps({ runtime: "edge", entryKey: "mw" }, { edge }),
    );

    expect(calls).toBe(1);
    expect(await res.text()).toBe("the-file");
  });

  it("still calls deps.edge for a legacy manifest with no runtime field at all", async () => {
    let calls = 0;
    const edge: RouteDeps["edge"] = async () => {
      calls++;
      return new Response(null, { headers: { "x-middleware-next": "1" } });
    };
    const res = await serve(
      new Request("https://app.example/static.txt"),
      edgeStaticDeps({ entryKey: "mw" }, { edge }),
    );

    expect(calls).toBe(1);
    expect(await res.text()).toBe("the-file");
  });

  it("500s and never forwards to a Function URL when deps.edge is unbound", async () => {
    const origin = fakeOrigin(() => new Response("should not be called"));
    const res = await serve(
      new Request("https://app.example/static.txt"),
      edgeStaticDeps({ entryKey: "mw" }, { fetch: origin.fetch }),
    );

    expect(res.status).toBe(500);
    expect(origin.requests.length).toBe(0);
  });
});
