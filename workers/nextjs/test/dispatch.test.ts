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

  it("sets x-forwarded-host to the public host so Next's Server Action origin check passes", async () => {
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
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://cachelab.ocel.dev/api/documents", {
        method: "POST",
        headers: { origin: "https://cachelab.ocel.dev" },
      }),
      deps,
    );

    expect(captured?.headers.get("x-forwarded-host")).toBe("cachelab.ocel.dev");
    expect(captured?.headers.get("x-forwarded-proto")).toBe("https");
  });

  it("forwards a POST body intact after buffering it off the request stream", async () => {
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
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const payload = "name=cachelab&value=1";
    await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://cachelab.ocel.dev/api/documents", {
        method: "POST",
        body: payload,
      }),
      deps,
    );

    // Buffering the body must not drop or corrupt it: the origin still gets the
    // full payload. (The wire win — a fixed Content-Length instead of a chunked
    // stream — is not observable on an in-process Request.)
    expect(await captured?.text()).toBe(payload);
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

  it("forwards the RSC-family headers to a prerender origin past allowHeader", async () => {
    let captured: Request | undefined;
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
            // Next's own allowHeader for a prerender omits the RSC family.
            config: { allowHeader: ["host"] },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: missingCache(),
    });

    await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog?_rsc=abc", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "1",
          "next-router-state-tree": "%5B%22%22%5D",
        },
      }),
      deps,
    );

    expect(captured?.headers.get("rsc")).toBe("1");
    expect(captured?.headers.get("next-router-prefetch")).toBe("1");
    expect(captured?.headers.get("next-router-state-tree")).toBe("%5B%22%22%5D");
  });

  // Interception is wired as an origin tried before the Lambda. These prove the
  // dispatch-level contract: a clean hit serves without touching the Lambda, and
  // any interception miss falls open to it.
  const interceptionConfig = { prefix: "prod/p/app/build" };

  // A cache store fronting canned entries keyed by their object name, matching
  // the R2 binding the deploy provides as OCEL_CACHE_STORE.
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

  const entryKey = (routePath: string) =>
    `${interceptionConfig.prefix}/cache/${routePath}.cache.json`;

  function interceptDeps(
    lambdaBody: string,
    storeEntry: unknown | null,
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
        store: storeOf(storeEntry ? { [entryKey("blog")]: storeEntry } : {}),
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

    // Colo memo miss, served from the R2 store one tier down.
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>edge</html>");
    expect(lambdaCalls()).toBe(0);
  });

  it("falls open to the Lambda when interception misses in the store", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", null);

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await res.text()).toBe("from-lambda");
    expect(lambdaCalls()).toBe(1);
  });

  it("serves a stale complete entry from the store and refreshes via the Lambda behind the request", async () => {
    let lambda = 0;
    const pending: Promise<unknown>[] = [];
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
        return new Response("regenerated", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: {
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      },
      interception: {
        config: interceptionConfig,
        // 61s after the entry was written: stale, but no expiration cutoff.
        now: () => 1_000 + 61_000,
        store: storeOf({
          [entryKey("blog")]: {
            lastModified: 1_000,
            value: { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} },
          },
        }),
      },
    });

    const res = await dispatchBlog(deps);

    // Served stale from the store, never blocked on the Lambda.
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>edge</html>");

    await Promise.all(pending);
    // Exactly one background regeneration — the deduped refresh, nothing more.
    expect(lambda).toBe(1);
  });

  it("sends a runtime prefetch (next-router-prefetch: 2) to the Lambda, uncached", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", {
      lastModified: 1_000,
      value: {
        kind: "APP_PAGE",
        html: "<html>edge</html>",
        rscData: btoa("RSC"),
        status: 200,
        headers: {},
        postponed: "PP",
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog", {
        headers: { RSC: "1", "next-router-prefetch": "2" },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
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

  // A PPR entry (APP_PAGE with a postponed state) routes to the compose path:
  // the shell is served from the ISR read and the origin is POSTed a resume,
  // never a plain render. These assert that dispatch-level wiring.
  function pprDeps(opts: {
    resume: string;
    entryPath?: string;
    entry: Record<string, unknown> | null;
    dispatch?: Record<string, unknown>;
  }): { deps: RouteDeps; resumeRequests: () => Request[]; cachePuts: () => number } {
    const resumeRequests: Request[] = [];
    let puts = 0;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: opts.dispatch ?? {
          "/ppr": {
            kind: "prerender",
            id: "/ppr",
            config: {},
            fallback: { initialRevalidate: 60, initialExpiration: 3600 },
            pprChain: { headers: { "next-resume": "1" } },
          },
        },
      },
      functionUrls: { "/ppr": "https://fn.example.com", "/posts/[id]": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        resumeRequests.push(req);
        return new Response(opts.resume, { status: 200 });
      }) as unknown as typeof fetch,
      cache: {
        cache: {
          match: async () => undefined,
          put: async () => {
            puts++;
          },
        } as unknown as Cache,
        waitUntil: () => {},
      },
      interception: {
        config: interceptionConfig,
        now: () => 2_000,
        store: storeOf(
          opts.entry ? { [entryKey(opts.entryPath ?? "ppr")]: opts.entry } : {},
        ),
      },
    });
    return { deps, resumeRequests: () => resumeRequests, cachePuts: () => puts };
  }

  const pprShellEntry = {
    lastModified: 1_000,
    value: {
      kind: "APP_PAGE",
      html: "[shell]",
      postponed: "POSTPONED",
      status: 200,
      headers: {},
    },
  };

  const dispatchPpr = (deps: RouteDeps, headers?: Record<string, string>) =>
    dispatchResult(
      { resolvedPathname: "/ppr", invocationTarget: { pathname: "/ppr" } },
      new Request("https://app.example/ppr", { headers }),
      deps,
    );

  it("composes shell + resumed dynamic for a PPR entry and POSTs the resume", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellEntry,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    const [resume] = resumeRequests();
    expect(resume.method).toBe("POST");
    expect(resume.headers.get("next-resume")).toBe("1");
    expect(await resume.text()).toBe("POSTPONED");
  });

  it("serves a PPR prefetch as the static shell, never a resume", async () => {
    // A prefetch (Next-Router-Prefetch) wants only the cacheable static shell so
    // the client's router cache holds it and the eventual click reveals it
    // instantly. Resuming here renders per-visitor dynamic content the client
    // cannot cache, so the navigation blocks on a full response instead.
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: {
        lastModified: 1_000,
        value: {
          kind: "APP_PAGE",
          html: "[shell]",
          rscData: btoa("[rsc-shell]"),
          postponed: "POSTPONED",
          status: 200,
          headers: {},
        },
      },
    });

    const res = await dispatchPpr(deps, { rsc: "1", "next-router-prefetch": "1" });

    expect(resumeRequests()).toHaveLength(0);
    // A full-route prefetch served from the store is a PRERENDER, not a
    // per-visitor PPR compose.
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("[rsc-shell]");
  });

  it("never puts a composed PPR response into the colo cache", async () => {
    const { deps, cachePuts } = pprDeps({ resume: "[dynamic]", entry: pprShellEntry });

    const res = await dispatchPpr(deps);
    await res.text();

    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(cachePuts()).toBe(0);
  });

  it("forwards the client's cookie to the resume origin", async () => {
    const { deps, resumeRequests } = pprDeps({ resume: "[dynamic]", entry: pprShellEntry });

    await dispatchPpr(deps, { cookie: "session=abc" });

    expect(resumeRequests()[0].headers.get("cookie")).toBe("session=abc");
  });

  it("bypasses PPR entirely when the draft cookie is present", async () => {
    const { deps, resumeRequests } = pprDeps({ resume: "from-lambda", entry: pprShellEntry });

    const res = await dispatchPpr(deps, { cookie: "__prerender_bypass=1" });

    // Falls through to a plain render (GET), not a resume POST.
    expect(resumeRequests()[0].method).toBe("GET");
    expect(await res.text()).toBe("from-lambda");
  });

  it("resumes a concrete dynamic path from the [id] fallback shell", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entryPath: "posts/[id]",
      entry: pprShellEntry,
      dispatch: {
        "/posts/[id]": {
          kind: "prerender",
          id: "/posts/[id]",
          config: {},
          fallback: { initialRevalidate: 60 },
          pprChain: { headers: { "next-resume": "1" } },
        },
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/posts/[id]", invocationTarget: { pathname: "/posts/7" } },
      new Request("https://app.example/posts/7"),
      deps,
    );

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()[0].method).toBe("POST");
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
