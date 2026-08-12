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

function mwResponse(
  headers: Record<string, string> = {},
  init: { status?: number; body?: BodyInit | null } = {},
): Response {
  return new Response(init.body ?? null, {
    status: init.status ?? 200,
    headers: { ...headers, "x-ocel-middleware-headers": Object.keys(headers).join(",") },
  });
}

function transportResponse(
  headers: Record<string, string> = {},
  init: { status?: number; body?: BodyInit | null } = {},
): Response {
  return new Response(init.body ?? null, { status: init.status ?? 200, headers });
}

const passthroughBody =
  'async () => new Response(null, { headers: { "x-middleware-next": "1", "x-mw-ran": "1", "x-ocel-middleware-headers": "x-middleware-next,x-mw-ran" } })';

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
      () => mwResponse({ "x-middleware-next": "1", "x-mw-ran": "1" }),
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
      () => mwResponse({ "x-middleware-next": "1" }),
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
      () => mwResponse({ "x-middleware-next": "1" }),
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
        mwResponse(
          { "content-type": "application/json" },
          { status: 401, body: JSON.stringify({ error: "nope" }) },
        ),
    );
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(401);
    expect(res.headers.get("content-type")).toBe("application/json");
    expect(await res.json()).toEqual({ error: "nope" });
  });

  it("preserves a middleware-authored content-type on the respond-directly path even though transport headers around it are dropped", async () => {
    const origin = fakeOrigin(
      () =>
        new Response(JSON.stringify({ error: "nope" }), {
          status: 401,
          headers: {
            "content-type": "application/json",
            "x-ocel-middleware-headers": "content-type",
            etag: '"abc123"',
            date: "Tue, 12 Aug 2026 00:00:00 GMT",
            connection: "keep-alive",
            "x-amzn-requestid": "req-1",
          },
        }),
    );
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(401);
    expect(res.headers.get("content-type")).toBe("application/json");
    expect(res.headers.has("etag")).toBe(false);
    expect(res.headers.has("date")).toBe(false);
    expect(res.headers.has("connection")).toBe(false);
    expect(res.headers.has("x-amzn-requestid")).toBe(false);
    expect(await res.json()).toEqual({ error: "nope" });
  });

  it("redirects with the middleware's Set-Cookie intact", async () => {
    const origin = fakeOrigin(
      () =>
        mwResponse(
          { location: "/login", "set-cookie": "sid=abc; Path=/" },
          { status: 307 },
        ),
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
        return mwResponse({
          "x-middleware-next": "1",
          "x-middleware-override-headers": "x-user",
          "x-middleware-request-x-user": "alice",
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
        return mwResponse({ "x-middleware-rewrite": "/b/" });
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
      () => mwResponse({ "x-middleware-next": "1" }),
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
      () => mwResponse({ "x-middleware-next": "1" }),
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
      () => mwResponse({ "x-middleware-next": "1" }),
    );
    await serve(
      new Request("https://app.example/_next/data/t/static.txt.json"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(origin.requests[0].headers.get("x-nextjs-data")).toBe("1");
  });

  it("does not set x-nextjs-data on an ordinary document request", async () => {
    const origin = fakeOrigin(
      () => mwResponse({ "x-middleware-next": "1" }),
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
        ? mwResponse({ "x-nextjs-redirect": "/somewhere" }, { status: 307 })
        : mwResponse({ location: "/somewhere" }, { status: 307 }),
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
      return mwResponse({ "x-middleware-next": "1", "x-mw-ran": "1" });
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
      () => mwResponse({}, { status: 500, body: "app error" }),
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
        mwResponse({ "retry-after": "60" }, { status: 429, body: "rate limited" }),
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

describe("the Function URL hop's transport headers stay off both the client response and the forwarded request", () => {
  it("does not leak content-type, content-length, etag, date, connection, cf-cache-status or x-amzn-* onto the client response", async () => {
    const origin = fakeOrigin(() =>
      transportResponse({
        "x-middleware-next": "1",
        link: "</style.css>; rel=preload",
        "x-ocel-middleware-headers": "x-middleware-next,link",
        "content-type": "application/json",
        "content-length": "0",
        etag: '"abc"',
        date: "Tue, 12 Aug 2026 00:00:00 GMT",
        connection: "keep-alive",
        "cf-cache-status": "DYNAMIC",
        "x-amzn-requestid": "req-1",
        "x-amzn-remapped-content-length": "0",
      }),
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.headers.get("link")).toBe("</style.css>; rel=preload");
    expect(res.headers.get("content-type")).not.toBe("application/json");
    for (const name of [
      "etag",
      "date",
      "connection",
      "cf-cache-status",
      "x-amzn-requestid",
      "x-amzn-remapped-content-length",
      "x-ocel-middleware-headers",
    ]) {
      expect(res.headers.has(name)).toBe(false);
    }
    expect(await res.text()).toBe("the-file");
  });

  it("does not overwrite the forwarded request's real content-type with the hop's synthetic one", async () => {
    const origin = fakeOrigin((req) => {
      if (new URL(req.url).host === new URL(MW_URL).host) {
        return transportResponse({
          "x-middleware-next": "1",
          "x-ocel-middleware-headers": "x-middleware-next",
          "content-type": "application/json",
        });
      }
      return new Response(req.headers.get("content-type") ?? "none");
    });

    const res = await serve(
      new Request("https://app.example/api/upload", {
        method: "POST",
        headers: { "content-type": "multipart/form-data; boundary=x" },
        body: "irrelevant",
      }),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/api/upload"],
          routes: emptyRoutes,
          dispatch: { "/api/upload": { kind: "lambda", id: "/api/upload" } },
          middleware: nodeMiddleware(),
        },
        functionUrls: { [MW_ID]: MW_URL, "/api/upload": "https://fn.example.com" },
        fetch: origin.fetch,
      } as Partial<RouteDeps>),
    );

    expect(await res.text()).toBe("multipart/form-data; boundary=x");
  });

  it("still honours x-middleware-next from an old bundle with no declaration, while dropping the hop's synthetic content-type", async () => {
    const origin = fakeOrigin(() =>
      transportResponse({
        "x-middleware-next": "1",
        "content-type": "application/json",
      }),
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps(nodeMiddleware(), { fetch: origin.fetch }),
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).not.toBe("application/json");
    expect(await res.text()).toBe("the-file");
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
