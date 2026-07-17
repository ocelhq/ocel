import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";

// A Fetcher-like stub returning a fixed status/body for every request.
function assetsReturning(status: number, body = ""): RouteDeps["assets"] {
  return {
    fetch: async () => new Response(body, { status }),
  };
}

function baseDeps(overrides: Partial<RouteDeps> = {}): RouteDeps {
  return {
    manifest: {
      buildId: "test",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {},
    },
    functionUrls: {},
    assets: assetsReturning(404),
    ...overrides,
  };
}

describe("dispatchResult", () => {
  it("serves a static route from the Assets binding", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/next.svg": { kind: "static" } },
      },
      assets: assetsReturning(200, "<svg/>"),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/next.svg" },
      new Request("https://app.example/next.svg"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
  });

  it("forwards a lambda route to its Function URL, preserving path and query", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
      },
      functionUrls: { "/api/documents": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("from-lambda", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://app.example/api/documents?q=1"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-lambda");
    expect(captured?.url).toBe("https://fn.example.com/api/documents?q=1");
  });

  it("invokes the parent function for a prerender route until ISR lands", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/": { kind: "prerender", id: "/" } },
      },
      functionUrls: { "/": "https://fn.example.com" },
      fetch: (async () => new Response("rendered", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("rendered");
  });

  // A cache that always misses, so a prerender route that is NOT bypassed goes
  // through serveCached and comes back stamped x-ocel-cache; a bypassed route
  // returns the origin response directly, with no such header.
  function missingCache(): NonNullable<RouteDeps["cache"]> {
    return {
      cache: {
        match: async () => undefined,
        put: async () => {},
      } as unknown as Cache,
      waitUntil: () => {},
    };
  }

  function bypassDeps(bypassKey: string): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/preview": {
            kind: "prerender",
            id: "/preview",
            config: { bypassFor: [{ type: "cookie", key: bypassKey }] },
          },
        },
      },
      functionUrls: { "/preview": "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: missingCache(),
    });
  }

  async function dispatchPreview(deps: RouteDeps, cookie: string) {
    return dispatchResult(
      { resolvedPathname: "/preview", invocationTarget: { pathname: "/preview" } },
      new Request("https://app.example/preview", { headers: { cookie } }),
      deps,
    );
  }

  it("does not treat a valueless cookie as a bypass match on a key prefix", async () => {
    // "badcookie" has no '='; it must not match bypass.key "badcooki".
    const res = await dispatchPreview(bypassDeps("badcooki"), "badcookie");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("bypasses the cache when a real bypass cookie is present", async () => {
    const res = await dispatchPreview(bypassDeps("preview"), "preview=1");
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  // Interception is wired as an origin tried before the Lambda. These prove the
  // dispatch-level contract: a clean hit serves without touching the Lambda, and
  // any interception miss falls open to it.
  const interceptionConfig = {
    accessKeyId: "a",
    secretKey: "s",
    region: "us-east-1",
    bucket: "assets",
    table: "state",
    prefix: "prod/p/app/build",
    tagNamespace: "TAG#prod#p#app#build#",
  };

  function interceptDeps(
    lambdaBody: string,
    s3Entry: unknown | null,
  ): { deps: RouteDeps; lambdaCalls: () => number } {
    let lambda = 0;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async () => {
        lambda++;
        return new Response(lambdaBody, {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: missingCache(),
      interception: {
        config: interceptionConfig,
        now: () => 2_000,
        signedFetch: (async (input: RequestInfo | URL) => {
          const url = typeof input === "string" ? input : input.toString();
          if (url.includes(".s3.")) {
            return s3Entry
              ? new Response(JSON.stringify(s3Entry), { status: 200 })
              : new Response("", { status: 404 });
          }
          return new Response(JSON.stringify({ Responses: {} }), { status: 200 });
        }) as typeof fetch,
      },
    });
    return { deps, lambdaCalls: () => lambda };
  }

  const dispatchBlog = (deps: RouteDeps) =>
    dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog"),
      deps,
    );

  it("serves a prerender from interception without invoking the Lambda", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", {
      lastModified: 1_000,
      value: { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} },
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("MISS"); // memo miss, served via edge
    expect(await res.text()).toBe("<html>edge</html>");
    expect(lambdaCalls()).toBe(0);
  });

  it("falls open to the Lambda when interception misses in S3", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", null);

    const res = await dispatchBlog(deps);

    expect(await res.text()).toBe("from-lambda");
    expect(lambdaCalls()).toBe(1);
  });

  it("skips interception for a pages-router _next/data request (serves JSON via Lambda)", async () => {
    // A data request would resolve to the same /blog prerender target, but must
    // be answered with pageData JSON, not the html interception reconstructs.
    const { deps, lambdaCalls } = interceptDeps("from-lambda", {
      lastModified: 1_000,
      value: { kind: "PAGES", html: "<html>edge</html>", status: 200, headers: {} },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/_next/data/t/blog.json"),
      deps,
    );

    expect(await res.text()).toBe("from-lambda");
    expect(lambdaCalls()).toBe(1);
  });

  it("returns 502 when a lambda route has no Function URL", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/x": { kind: "lambda", id: "/api/x" } },
      },
      functionUrls: {},
    });

    const res = await dispatchResult(
      { resolvedPathname: "/api/x", invocationTarget: { pathname: "/api/x" } },
      new Request("https://app.example/api/x"),
      deps,
    );

    expect(res.status).toBe(502);
  });

  it("falls back to the Assets binding when the path is not in the manifest", async () => {
    const deps = baseDeps({ assets: assetsReturning(200, "found") });

    const res = await dispatchResult(
      { resolvedPathname: "/unenumerated.txt" },
      new Request("https://app.example/unenumerated.txt"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("found");
  });

  it("returns 404 when neither the manifest nor the Assets binding has the path", async () => {
    const deps = baseDeps({ assets: assetsReturning(404) });

    const res = await dispatchResult(
      { resolvedPathname: "/missing" },
      new Request("https://app.example/missing"),
      deps,
    );

    expect(res.status).toBe(404);
  });

  it("falls back to Assets when routing produced no resolved pathname", async () => {
    const deps = baseDeps({ assets: assetsReturning(200, "asset") });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/whatever"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("asset");
  });

  it("emits a redirect response", async () => {
    const res = await dispatchResult(
      { redirect: { url: "https://app.example/new", status: 308 } },
      new Request("https://app.example/old"),
      baseDeps(),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("https://app.example/new");
  });
});
