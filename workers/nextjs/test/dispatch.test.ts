import { tagSnapshotKey } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";
import { sentinelUrl } from "../src/cache";
import { coloDeps } from "./cache-deps";
import type { AssetBucket } from "../src/assets";

// An in-memory R2-bucket-shaped store, keyed by "<prefix><pathname>" exactly as
// serveStaticAsset composes it, fronting no real Cache API (match always
// misses) so every call is a fresh store read.
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

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
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
    slug: "p1",
    app: "web",
    assetStore: noAssets(),
    ...overrides,
  };
}

describe("dispatchResult", () => {
  it("serves a static route from the R2 asset store", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/next.svg": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/next.svg": "<svg/>" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/next.svg" },
      new Request("https://app.example/next.svg"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
  });

  // The membrane sends an empty body as one sentinel byte because a Function URL
  // never terminates a bodyless streamed response; leaving the byte in place
  // would serve it as the response's content.
  it.each([200, 307, 404, 405, 500])(
    "restores the empty body a %i sentinel response stands for",
    async (status) => {
      const deps = baseDeps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/status": { kind: "lambda", id: "/status" } },
        },
        functionUrls: { "/status": "https://fn.example.com" },
        fetch: (async () =>
          new Response("\n", {
            status,
            headers: { "x-ocel-empty-body": "1", "x-custom": "kept" },
          })) as unknown as typeof fetch,
      });

      const res = await dispatchResult(
        { resolvedPathname: "/status", invocationTarget: { pathname: "/status" } },
        new Request("https://app.example/status"),
        deps,
      );

      expect(res.status).toBe(status);
      expect(await res.text()).toBe("");
      expect(res.headers.get("x-ocel-empty-body")).toBeNull();
      expect(res.headers.get("x-custom")).toBe("kept");
    },
  );

  it("leaves a body that is genuinely one byte alone", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/tiny": { kind: "lambda", id: "/tiny" } },
      },
      functionUrls: { "/tiny": "https://fn.example.com" },
      fetch: (async () => new Response("\n", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/tiny", invocationTarget: { pathname: "/tiny" } },
      new Request("https://app.example/tiny"),
      deps,
    );

    expect(await res.text()).toBe("\n");
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

  it("forwards a lambda route through originFetch (signed), not plain fetch", async () => {
    let signedUrl: string | undefined;
    let plainCalled = false;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
      },
      functionUrls: { "/api/documents": "https://fn.example.com" },
      fetch: (async () => {
        plainCalled = true;
        return new Response("plain", { status: 200 });
      }) as unknown as typeof fetch,
      originFetch: (async (req: Request) => {
        signedUrl = req.url;
        return new Response("signed", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://app.example/api/documents"),
      deps,
    );

    expect(await res.text()).toBe("signed");
    expect(signedUrl).toBe("https://fn.example.com/api/documents");
    expect(plainCalled).toBe(false);
  });

  it("forwards an external rewrite through plain fetch, never originFetch", async () => {
    let plainUrl: string | undefined;
    let signedCalled = false;
    const deps = baseDeps({
      fetch: (async (req: Request) => {
        plainUrl = req.url;
        return new Response("external", { status: 200 });
      }) as unknown as typeof fetch,
      originFetch: (async () => {
        signedCalled = true;
        return new Response("signed", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { externalRewrite: "https://other.example/proxied" },
      new Request("https://app.example/x"),
      deps,
    );

    expect(await res.text()).toBe("external");
    expect(plainUrl).toBe("https://other.example/proxied");
    expect(signedCalled).toBe(false);
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
    return coloDeps({
      cache: {
        match: async () => undefined,
        put: async () => {},
      } as unknown as Cache,
      waitUntil: () => {},
    });
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

  // A prerender route whose allowHeader is the one Next actually emits — which
  // omits `cookie`, so anything that must reach the origin with its cookies has
  // to leave through the bypass path rather than the filtered one.
  function draftDeps(capture: (req: Request) => void): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/ssg-draft-mode/test-1": {
            kind: "prerender",
            id: "/ssg-draft-mode/[[...route]]",
            config: {
              allowHeader: ["host", "x-matched-path", "x-prerender-revalidate"],
              bypassFor: [{ type: "header", key: "next-action" }],
            },
          },
        },
      },
      functionUrls: { "/ssg-draft-mode/[[...route]]": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        capture(req);
        return new Response("rendered", { status: 200 });
      }) as unknown as typeof fetch,
      cache: missingCache(),
    });
  }

  function draftRequest(init: RequestInit = {}) {
    return new Request("https://app.example/ssg-draft-mode/test-1", {
      headers: { cookie: "__prerender_bypass=abc123" },
      ...init,
    });
  }

  async function dispatchDraft(deps: RouteDeps, request: Request) {
    return dispatchResult(
      {
        resolvedPathname: "/ssg-draft-mode/test-1",
        invocationTarget: { pathname: "/ssg-draft-mode/test-1" },
      },
      request,
      deps,
    );
  }

  it("forwards the draft cookie to a prerender origin, which allowHeader omits", async () => {
    let captured: Request | undefined;
    const res = await dispatchDraft(
      draftDeps((req) => (captured = req)),
      draftRequest(),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(captured?.headers.get("cookie")).toBe("__prerender_bypass=abc123");
  });

  it("forwards a non-GET to a prerender origin with its own headers", async () => {
    let captured: Request | undefined;
    const res = await dispatchDraft(
      draftDeps((req) => (captured = req)),
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { cookie: "session=xyz" },
        body: "{}",
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(captured?.headers.get("cookie")).toBe("session=xyz");
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
  const interceptionConfig = { isrPrefix: "prod/p/app/build" };

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
    `${interceptionConfig.isrPrefix}/cache/${routePath}.cache.json`;

  function interceptDeps(
    lambdaBody: string,
    storeEntry: unknown | null,
    lambdaHeaders: Record<string, string> = {},
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
          headers: { "cache-control": "s-maxage=60", ...lambdaHeaders },
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
    // A fresh entry, so the freshness Next's own server would have reported.
    expect(res.headers.get("x-nextjs-cache")).toBe("HIT");
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

  it("leaves the Lambda's own x-nextjs-cache alone on a store miss", async () => {
    // REVALIDATED is a value only Next's server can know to report, so a serve
    // it authored has to reach the client with that value intact.
    const { deps } = interceptDeps("from-lambda", null, {
      "x-nextjs-cache": "REVALIDATED",
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-nextjs-cache")).toBe("REVALIDATED");
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
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
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
    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>edge</html>");

    await Promise.all(pending);
    // Exactly one background regeneration — the deduped refresh, nothing more.
    expect(lambda).toBe(1);
  });

  // A Cache whose only content is the sentinel for `refreshKey`: this colo has
  // already admitted a refresh of the route, and no entry is stored, so the
  // request is answered exactly as it would be otherwise.
  function coloHoldingSentinel(refreshKey: string): Cache {
    const url = sentinelUrl(refreshKey);
    return {
      match: async (request: Request) =>
        request.url === url ? new Response(null) : undefined,
      put: async () => {},
      delete: async () => false,
    } as unknown as Cache;
  }

  it("leaves the stale R2 refresh to whichever isolate holds the colo's sentinel", async () => {
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
      cache: coloDeps({
        cache: coloHoldingSentinel("t:/blog"),
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
      interception: {
        config: interceptionConfig,
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

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>edge</html>");

    await Promise.all(pending);
    expect(lambda).toBe(0);
  });

  it("leaves a stale PPR shell's refresh to whichever isolate holds the colo's sentinel", async () => {
    const origins: Request[] = [];
    const pending: Promise<unknown>[] = [];
    const pprDispatch = {
      "/ppr": {
        kind: "prerender",
        id: "/ppr",
        config: {},
        fallback: { initialRevalidate: 60, initialExpiration: 3600 },
        pprChain: { headers: { "next-resume": "1" } },
      },
    };
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: pprDispatch,
      },
      functionUrls: { "/ppr": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        origins.push(req);
        return new Response("[dynamic]", { status: 200 });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: coloHoldingSentinel("t:/ppr"),
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
      interception: {
        config: interceptionConfig,
        // 61s past the entry: the shell is stale and would otherwise be refreshed.
        now: () => 1_000 + 61_000,
        store: storeOf({
          [entryKey("ppr")]: {
            lastModified: 1_000,
            value: {
              kind: "APP_PAGE",
              html: "[shell]",
              postponed: "POSTPONED",
              status: 200,
              headers: {},
            },
          },
        }),
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/ppr", invocationTarget: { pathname: "/ppr" } },
      new Request("https://app.example/ppr"),
      deps,
    );

    expect(await res.text()).toBe("[shell][dynamic]");
    await Promise.all(pending);
    // The resume POST is the visitor's own render and always happens; the
    // background regeneration is the one the sentinel suppressed.
    expect(origins.map((req) => req.method)).toEqual(["POST"]);
  });

  it("refreshes the colo entry with the Lambda's fresh body after a stale R2 hit", async () => {
    const pending: Promise<unknown>[] = [];
    const stored = new Map<string, Response>();
    // An externally-controlled deferred stands in for the Lambda round-trip:
    // the test resolves it explicitly, after the stale serve (and its
    // populate-on-serve colo write) has already completed, so the ordering
    // between the two colo writes is driven by explicit control flow rather
    // than a wall-clock delay.
    let resolveLambda!: (response: Response) => void;
    const lambdaResponse = new Promise<Response>((resolve) => {
      resolveLambda = resolve;
    });
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
      fetch: (() => lambdaResponse) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (req: Request) => stored.get(req.url)?.clone(),
          put: async (req: Request, res: Response) => {
            stored.set(req.url, res);
          },
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
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

    // Served immediately from the stale R2 entry; the populate-on-serve write
    // (of that same stale body) into colo has already run synchronously by
    // this point, ahead of the still-pending background refresh.
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>edge</html>");

    // Now let the Lambda round-trip complete and drain the background refresh.
    resolveLambda(
      new Response("fresh-lambda-body", {
        status: 200,
        headers: { "cache-control": "s-maxage=60" },
      }),
    );
    await Promise.all(pending);

    // The self-healing invariant: a follow-up request converges on the fresh
    // body straight from colo (a HIT, no further Lambda round-trip), proving
    // the background refresh wrote the Lambda's fresh body into colo rather
    // than discarding it.
    const follow = await dispatchBlog(deps);
    expect(follow.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await follow.text()).toBe("fresh-lambda-body");
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
    // When set, the resume must ride the signed origin seam: bind an originFetch
    // spy and leave plain fetch as a tripwire that must never be called.
    signed?: boolean;
  }): {
    deps: RouteDeps;
    resumeRequests: () => Request[];
    cachePuts: () => number;
    plainCalled: () => boolean;
  } {
    const resumeRequests: Request[] = [];
    let puts = 0;
    let plainCalled = false;
    const record = (async (req: Request) => {
      resumeRequests.push(req);
      return new Response(opts.resume, { status: 200 });
    }) as unknown as typeof fetch;
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
      fetch: opts.signed
        ? ((async (req: Request) => {
            plainCalled = true;
            return record(req);
          }) as unknown as typeof fetch)
        : record,
      originFetch: opts.signed ? record : undefined,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {
            puts++;
          },
        } as unknown as Cache,
        waitUntil: () => {},
      }),
      interception: {
        config: interceptionConfig,
        now: () => 2_000,
        store: storeOf(
          opts.entry ? { [entryKey(opts.entryPath ?? "ppr")]: opts.entry } : {},
        ),
      },
    });
    return {
      deps,
      resumeRequests: () => resumeRequests,
      cachePuts: () => puts,
      plainCalled: () => plainCalled,
    };
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

  it("POSTs the resume through the signed origin seam, never plain fetch", async () => {
    // The resume is a Function-URL forward like any other, so it must be signed
    // when edge credentials are bound. It shares dispatchPrerender's origin fetch
    // with every other forward, so this asserts it rides that seam rather than
    // leaking out as an unsigned POST that the AWS_IAM Function URL would 403.
    const { deps, resumeRequests, plainCalled } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellEntry,
      signed: true,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()).toHaveLength(1);
    expect(resumeRequests()[0].method).toBe("POST");
    expect(plainCalled()).toBe(false);
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

  it("falls back to the R2 asset store when the path is not in the manifest", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({ "/unenumerated.txt": "found" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/unenumerated.txt" },
      new Request("https://app.example/unenumerated.txt"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("found");
  });

  it("returns 404 when neither the manifest nor the R2 asset store has the path", async () => {
    const deps = baseDeps({ assetStore: assetStoreServing({}) });

    const res = await dispatchResult(
      { resolvedPathname: "/missing" },
      new Request("https://app.example/missing"),
      deps,
    );

    expect(res.status).toBe(404);
  });

  it("falls back to the R2 asset store when routing produced no resolved pathname", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({ "/whatever": "asset" }),
    });

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

  it("tags a matched route with x-matched-path using the resolved template", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/posts/[id]": { kind: "lambda", id: "/posts/[id]" } },
      },
      functionUrls: { "/posts/[id]": "https://fn.example.com" },
      fetch: (async () => new Response("ok", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/posts/[id]", invocationTarget: { pathname: "/posts/7" } },
      new Request("https://app.example/posts/7"),
      deps,
    );

    expect(res.headers.get("x-matched-path")).toBe("/posts/[id]");
  });

  it("omits x-matched-path when routing produced no resolved pathname", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({ "/whatever": "asset" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/whatever"),
      deps,
    );

    expect(res.headers.has("x-matched-path")).toBe(false);
  });
});

// A Server Action invalidates a tag at the origin, which republishes the edge's
// tag-clock replica. The colo the action travelled through is fronting that
// replica with a TTL'd Cache API copy, so without an explicit purge it keeps
// answering "nothing was invalidated" for the whole TTL — and a fully static
// route (initialRevalidate false) has no other way to go stale, so the visitor
// who raised the invalidation is served their pre-invalidation page.
describe("a Server Action's invalidation reaching the colo it travelled through", () => {
  const cfg = { isrPrefix: "prod/p/app/build" };

  function scenario() {
    let now = 10_000;
    let lambdaCalls = 0;
    let actionRevalidates = true;
    let replica = JSON.stringify({
      version: 1,
      deployedAt: 0,
      generatedAt: 900,
      records: {} as Record<string, { expired?: number }>,
    });

    const store = {
      async get(key: string) {
        if (key !== tagSnapshotKey(cfg.isrPrefix)) return null;
        return { etag: '"v"', text: async () => replica };
      },
    };

    // A PoP cache that really retains what it is handed — which is the whole of
    // what makes a stale replica observable.
    const pop = new Map<string, string>();
    const snapshotCache = {
      async match(request: Request) {
        const body = pop.get(request.url);
        return body === undefined ? undefined : new Response(body);
      },
      async put(request: Request, response: Response) {
        pop.set(request.url, await response.text());
      },
      async delete(request: Request) {
        return pop.delete(request.url);
      },
    };

    const colo = new Map<string, Response>();
    const pending: Promise<unknown>[] = [];
    // The snapshot prime is deliberately fire-and-forget on the request path
    // (interception.ts), so draining waitUntil alone does not mean the replica
    // has been cached yet; yielding to the event loop is what covers it.
    const settle = async () => {
      while (pending.length) await pending.shift();
      await new Promise((resolve) => setTimeout(resolve, 0));
    };

    const deps = baseDeps({
      manifest: {
        buildId: "test",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            tags: ["posts"],
            config: { renderingMode: "STATIC" },
            fallback: { initialRevalidate: false },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async (request: Request) => {
        lambdaCalls++;
        if (request.method === "POST") {
          // What Next stamps on a Server Action response that invalidated
          // something, by which point the origin has republished the replica.
          return new Response("action", {
            status: 200,
            headers: actionRevalidates ? { "x-action-revalidated": "1" } : {},
          });
        }
        return new Response("page", {
          status: 200,
          headers: {
            "cache-control": "s-maxage=31536000",
            "x-nextjs-cache": "HIT",
          },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (request: Request) => colo.get(request.url)?.clone(),
          put: async (request: Request, response: Response) => {
            colo.set(request.url, response);
          },
        } as unknown as Cache,
        waitUntil: (promise: Promise<unknown>) => {
          pending.push(promise);
        },
        now: () => now,
      }),
      interception: {
        config: cfg,
        store,
        snapshotCache,
        now: () => now,
        waitUntil: (promise: Promise<unknown>) => {
          pending.push(promise);
        },
      },
    });

    const get = () =>
      dispatchResult(
        { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
        new Request("https://app.example/blog"),
        deps,
      );

    const runAction = () =>
      dispatchResult(
        { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
        new Request("https://app.example/blog", {
          method: "POST",
          headers: { "next-action": "abc" },
        }),
        deps,
      );

    return {
      get,
      runAction,
      settle,
      lambdaCalls: () => lambdaCalls,
      advanceTo: (at: number) => {
        now = at;
      },
      actionRevalidatesNothing: () => {
        actionRevalidates = false;
      },
      invalidate: (at: number) => {
        replica = JSON.stringify({
          version: 1,
          deployedAt: 0,
          generatedAt: at,
          records: { posts: { expired: at } },
        });
      },
    };
  }

  it("serves the invalidated entry as stale on the next request", async () => {
    const s = scenario();

    // Populates the colo entry (written at 10_000) and the PoP replica copy.
    await s.get();
    await s.settle();
    expect(s.lambdaCalls()).toBe(1);

    s.invalidate(20_000);
    s.advanceTo(15_000);
    const action = await s.runAction();
    expect(action.headers.get("x-action-revalidated")).toBe("1");
    await s.settle();
    expect(s.lambdaCalls()).toBe(2);

    // Past the isolate memo's window, so the PoP copy is the only thing that
    // could still answer from before the invalidation.
    s.advanceTo(30_000);
    const after = await s.get();

    expect(after.headers.get("x-nextjs-cache")).toBe("STALE");
    await s.settle();
    expect(s.lambdaCalls()).toBe(3);
  });

  // The control for the test above: with the purge withheld, the PoP copy is
  // demonstrably what answers, so the staleness that test proves gone is this
  // one and not the isolate memo lapsing on its own.
  it("keeps answering from the cached replica when the action revalidated nothing", async () => {
    const s = scenario();
    s.actionRevalidatesNothing();

    await s.get();
    await s.settle();

    s.invalidate(20_000);
    s.advanceTo(15_000);
    const action = await s.runAction();
    expect(action.headers.has("x-action-revalidated")).toBe(false);
    await s.settle();

    s.advanceTo(30_000);
    const after = await s.get();

    expect(after.headers.get("x-nextjs-cache")).toBe("HIT");
  });
});
