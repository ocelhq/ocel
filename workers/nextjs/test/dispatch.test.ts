import type { Route } from "@next/routing";
import { tagSnapshotKey } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import { dispatchResult, serve, type RouteDeps } from "../src/index";
import { refreshBackoffSeconds, sentinelUrl } from "../src/cache";
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

  // A statically-optimized dynamic page is one document answering every path its
  // template spans, stored under the template's own name — so the asset is read
  // under the manifest key the target was found at, not the path requested.
  it("serves a statically-optimized dynamic page under its manifest key", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "/docs",
        pathnames: ["/docs/[slug]"],
        routes: {},
        dispatch: { "/docs/[slug]": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/docs/[slug].html": "<html>slug</html>" }),
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/docs/[slug]",
        invocationTarget: { pathname: "/docs/slug-1" },
      },
      new Request("https://app.example/docs/slug-1"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>slug</html>");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(res.headers.get("x-matched-path")).toBe("/docs/[slug]");
  });

  it("serves an ordinary static page under a basePath", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "/docs",
        pathnames: ["/docs/hello"],
        routes: {},
        dispatch: { "/docs/hello": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/docs/hello.html": "<html>hello</html>" }),
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/docs/hello",
        invocationTarget: { pathname: "/docs/hello" },
      },
      new Request("https://app.example/docs/hello"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>hello</html>");
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

  // Once the Lambda handler runs in minimal mode, Next stamps this header on
  // SSG responses as its contract with the platform — the platform is meant to
  // consume it, not let it leak to a client.
  it("strips x-next-cache-tags from a Lambda-forwarded response", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/tags": { kind: "lambda", id: "/tags" } },
      },
      functionUrls: { "/tags": "https://fn.example.com" },
      fetch: (async () =>
        new Response("from-lambda", {
          status: 200,
          headers: { "x-next-cache-tags": "tag1,tag2", "x-custom": "kept" },
        })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/tags", invocationTarget: { pathname: "/tags" } },
      new Request("https://app.example/tags"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-lambda");
    expect(res.headers.get("x-next-cache-tags")).toBeNull();
    expect(res.headers.get("x-custom")).toBe("kept");
  });

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

  // "Its own headers" stops at next-resume. The membrane runs a request bearing
  // it under minimal mode — Next's caching, fallback and revalidation handed to
  // the platform — and a non-GET is forwarded here under the client's headers.
  // Forged onto a dynamic SSG route it would skip the fallback check that 404s a
  // path generateStaticParams never produced, and have Next render and cache it.
  it("drops a client-forged next-resume from a non-GET prerender forward", async () => {
    let captured: Request | undefined;
    await dispatchDraft(
      draftDeps((req) => (captured = req)),
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { "next-resume": "1" },
        body: "[1,{}]",
      }),
    );

    expect(captured?.headers.get("next-resume")).toBeNull();
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

  const dispatchBlog = (
    deps: RouteDeps,
    request = new Request("https://app.example/blog"),
  ) =>
    dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      request,
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

  // The colo tier caps its own draw on the entry it holds; these two sites
  // refresh an entry the tier BELOW holds, so the bound has to travel out of
  // intercept and into admitRefresh. Uncapped, a route whose stale window is
  // shorter than the jitter spends the tail of the wait past expiration, where
  // nothing dedupes and every isolate renders for itself.
  it.each([
    ["a complete entry", "/blog", { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} }],
    ["a PPR shell", "/ppr", { kind: "APP_PAGE", html: "[shell]", postponed: "POSTPONED", status: 200, headers: {} }],
  ])("caps the R2 tier's admission wait on %s's remaining stale window", async (_name, path, value) => {
    const bounds: number[] = [];
    const pending: Promise<unknown>[] = [];
    const id = path.slice(1);
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          [path]: {
            kind: "prerender",
            id: path,
            config: "postponed" in value ? { renderingMode: "PARTIALLY_STATIC" } : {},
            fallback: { initialRevalidate: 60, initialExpiration: 3600 },
          },
        },
      },
      functionUrls: { [path]: "https://fn.example.com" },
      fetch: (async () =>
        new Response("regenerated", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
        admissionDelay: (staleForMs: number) => {
          bounds.push(staleForMs);
          return Promise.resolve();
        },
      }),
      interception: {
        config: interceptionConfig,
        // 500ms short of expiration=3600s, so the draw may span 500ms, not 1s.
        now: () => 1_000 + 3_599_500,
        store: storeOf({ [entryKey(id)]: { lastModified: 1_000, value } }),
      },
    });

    await dispatchResult(
      { resolvedPathname: path, invocationTarget: { pathname: path } },
      new Request(`https://app.example${path}`),
      deps,
    );
    await Promise.all(pending);

    expect(bounds).toEqual([500]);
  });

  // An admitted refresh reaches the Lambda through originBlocking, which is
  // never routed through the R2 tier — so unless it reads R2 itself, every colo
  // that admits renders even when another colo rewrote the entry a moment
  // earlier. These drive the admission with the store rewritten (or not)
  // underneath it, which is exactly that race.
  function refreshOverStore(
    entries: Record<string, unknown>,
    // Another colo's refresh landing in R2 while this colo's admission is still
    // waiting out its jitter, which is the window the whole read exists for.
    // It is the whole entry value, not just its bytes, because a landing can
    // change the entry's shape — a redeploy that turns the route PPR lands a
    // postponed entry the colo's own variant cannot be refilled from.
    landsDuringWait?: Record<string, unknown>,
    lambdaStatus = 200,
    // When the landing entry claims to have been written. The default is `now`,
    // so it reads back fresh; an earlier one reads back stale, which is the
    // steady state of any route whose revalidate window is shorter than the
    // round trip from a landing to the next read of it.
    landsModified = 1_000 + 61_000,
  ) {
    let lambda = 0;
    const pending: Promise<unknown>[] = [];
    const puts: {
      url: string;
      body: string;
      cacheControl: string | null;
      entryModified: string | null;
    }[] = [];
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
          status: lambdaStatus,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async (request: Request, response: Response) => {
            puts.push({
              url: new Request(request).url,
              cacheControl: response.headers.get("cache-control"),
              entryModified: response.headers.get("x-ocel-entry-modified"),
              body: await response.text(),
            });
          },
          delete: async () => false,
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
        admissionDelay: async () => {
          if (landsDuringWait === undefined) return;
          entries[entryKey("blog")] = {
            lastModified: landsModified,
            value: landsDuringWait,
          };
        },
      }),
      interception: {
        config: interceptionConfig,
        // 61s past the entry below: stale, with no expiration cutoff.
        now: () => 1_000 + 61_000,
        store: storeOf(entries),
      },
    });
    return { deps, pending, puts, lambdaCalls: () => lambda };
  }

  const pageValue = (html: string, extra: Record<string, unknown> = {}) => ({
    kind: "APP_PAGE",
    html,
    status: 200,
    headers: {},
    ...extra,
  });

  const staleBelow = () => ({
    [entryKey("blog")]: { lastModified: 1_000, value: pageValue("<html>edge</html>") },
  });

  // A route whose entry carries a prerendered shell, which is what a full-route
  // prefetch is answered from.
  const stalePrefetchableBelow = () => ({
    [entryKey("blog")]: {
      lastModified: 1_000,
      value: pageValue("[shell]", {
        postponed: "POSTPONED",
        rscData: btoa("[shell-rsc]"),
      }),
    },
  });

  const prefetchRequest = () =>
    new Request("https://app.example/blog", {
      headers: { RSC: "1", "next-router-prefetch": "1" },
    });

  it("does not render when R2 already holds a fresher entry by the time the refresh is admitted", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>fresher</html>"),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
  });

  it("refills the colo entry from R2 rather than from a render it skipped", async () => {
    const { deps, pending, puts } = refreshOverStore(
      staleBelow(),
      pageValue("<html>fresher</html>"),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(puts.map((put) => put.body)).toContain("<html>fresher</html>");
  });

  it("backs the R2 tier's refresh off when the Lambda refuses it", async () => {
    // A 429 from an origin that is already shedding load used to DELETE the
    // colo's claim, so the next request re-admitted at once and the failure fed
    // the herd. The claim is re-armed for the backoff instead.
    const { deps, pending, puts } = refreshOverStore(staleBelow(), undefined, 429);

    await dispatchBlog(deps);
    await Promise.all(pending);

    // The claim itself is the first write under this key; the settlement — the
    // one that says how long the colo stays away — is the last.
    const sentinelWrites = puts.filter(
      (put) => put.url === sentinelUrl("t:/blog"),
    );
    expect(sentinelWrites.at(-1)?.cacheControl).toBe(
      `max-age=${refreshBackoffSeconds}`,
    );
  });

  it("renders when R2 is still stale by the time the refresh is admitted", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(staleBelow());

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  // A route whose revalidate window is shorter than the round trip is never
  // read back fresh: the entry below is newer than the one this tier holds, and
  // still stale. Refusing it on staleness alone freezes this tier's
  // lastModified forever — the colo goes on serving an ancient body until hard
  // expiry, and the dedup id derived from that lastModified freezes with it, so
  // the queue drops nearly every enqueue that would have unstuck it.
  it("promotes a stale entry below that is newer than the one being refreshed", async () => {
    const { deps, pending, puts, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>newer</html>"),
      200,
      // 60.5s before `now`: past revalidate=60, and newer than the 1_000 above.
      1_500,
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
    // Mirrored into the colo, dated by the entry it came from — that advance is
    // the whole point, since it is what the next enqueue's dedup id is built on.
    expect(
      puts.find((put) => put.body === "<html>newer</html>")?.entryModified,
    ).toBe(String(1_500));
  });

  it("renders when the stale entry below is older than the one being refreshed", async () => {
    const { deps, pending, puts, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>older</html>"),
      200,
      500,
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
    expect(puts.map((put) => put.body)).not.toContain("<html>older</html>");
  });

  // A prefetch is answered without a staleness gate — it serves whatever the
  // entry holds — and that ungated serve used to be reported as `stale: false`,
  // which is a different claim entirely. Next prefetches links aggressively, and
  // both prefetch variants are colo-cacheable, so reading that flag as "the
  // entry is fresh" strands the route: the refresh is never admitted, and where
  // it is, the tier-below read answers it with the same ancient entry.
  it("regenerates the stale entry a prefetch was served from", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(stalePrefetchableBelow());

    await dispatchBlog(deps, prefetchRequest());
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  it("still skips the render when a prefetch's entry was refreshed below during the wait", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      stalePrefetchableBelow(),
      pageValue("[fresher shell]", {
        postponed: "POSTPONED",
        rscData: btoa("[fresher-shell-rsc]"),
      }),
    );

    await dispatchBlog(deps, prefetchRequest());
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
  });

  // The colo dates an entry by the x-ocel-entry-modified the tier below stamps
  // on it; unstamped, it is dated "now" instead, and a prefetch variant mirrored
  // into the colo would be served for a fresh full window past the age it
  // actually has — stale bytes, restamped as fresh, once per mirror.
  it("dates a mirrored prefetch by the entry's own modified time, not by the mirror", async () => {
    const { deps, pending, puts } = refreshOverStore(
      stalePrefetchableBelow(),
      pageValue("[fresher shell]", {
        postponed: "POSTPONED",
        rscData: btoa("[fresher-shell-rsc]"),
      }),
    );

    await dispatchBlog(deps, prefetchRequest());
    await Promise.all(pending);

    // Two puts of the prefetch variant: the serve memoizing the stale entry it
    // answered from, and the refresh mirroring the fresher one. Each carries the
    // time of the entry it came from, and neither the wall clock.
    const mirrored = puts.filter((put) => put.url.endsWith(".prefetch.rsc"));
    expect(mirrored.map((put) => put.entryModified).sort()).toEqual([
      String(1_000),
      String(1_000 + 61_000),
    ]);
  });

  // The colo holds a complete variant; the entry below turns PPR under it (a
  // redeploy). The below read is fresh, but a shell cannot refill this colo's
  // variant — so claiming the refresh landed would hold the colo's route-wide
  // claim while leaving it serving the stale entry it already had.
  it("renders when the fresher entry below cannot refill the colo's variant", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("[shell]", { postponed: "POSTPONED" }),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  // The other half of that rule. A PPR navigation is per-visitor and never
  // colo-cached, so this colo holds no variant for the entry below to refill:
  // when that entry is fresh by the time the admission wakes, the render's only
  // effect would have been regenerating what R2 already holds, and skipping it
  // is exactly what the tier-below read exists for.
  it("skips the render when a variant with no colo entry is fresh below", async () => {
    const pprEntry = (html: string) => ({
      lastModified: html === "[shell]" ? 1_000 : 1_000 + 61_000,
      value: pageValue(html, {
        postponed: "POSTPONED",
        rscData: btoa(`${html}-rsc`),
      }),
    });
    const entries: Record<string, unknown> = { [entryKey("ppr")]: pprEntry("[shell]") };
    // Only the background revalidation carries x-prerender-revalidate; the PPR
    // resume the serve itself performs is a different call to the same origin.
    let revalidations = 0;
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/ppr": {
            kind: "prerender",
            id: "/ppr",
            config: { renderingMode: "PARTIALLY_STATIC" },
            fallback: { initialRevalidate: 60 },
            pprChain: { headers: {} },
          },
        },
      },
      functionUrls: { "/ppr": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        if (req.headers.has("x-prerender-revalidate")) revalidations++;
        return new Response("[dynamic]", { status: 200 });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
        admissionDelay: async () => {
          entries[entryKey("ppr")] = pprEntry("[fresher shell]");
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf(entries),
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/ppr", invocationTarget: { pathname: "/ppr" } },
      new Request("https://app.example/ppr", { headers: { RSC: "1" } }),
      deps,
    );
    await res.text();
    await Promise.all(pending);

    expect(revalidations).toBe(0);
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
    resumeHeaders?: Record<string, string>;
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
      return new Response(opts.resume, { status: 200, headers: opts.resumeHeaders });
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

  // Minimal mode makes Next stamp x-next-cache-tags on every SSG response it
  // renders — the platform's to consume, never a client's to see. The Lambda
  // leg has its own coverage above; this is the leg that reaches the origin
  // through dispatchPrerender's own signed fetch rather than that one.
  it("strips x-next-cache-tags from a prerender's origin render", async () => {
    const { deps } = pprDeps({
      resume: "[rendered]",
      resumeHeaders: { "x-next-cache-tags": "tag1,tag2", "x-custom": "kept" },
      entry: null,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[rendered]");
    expect(res.headers.get("x-next-cache-tags")).toBeNull();
    expect(res.headers.get("x-custom")).toBe("kept");
  });

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

  // The other half of dropping an inbound next-resume: the strip runs above
  // dispatch, and the resume chain stamps its own header below it, so the leg
  // that is genuinely a resume still declares itself.
  it("stamps next-resume on the real resume even when the client sent one", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellEntry,
    });

    const res = await dispatchPpr(deps, { "next-resume": "1" });

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()[0]?.headers.get("next-resume")).toBe("1");
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

  it("answers a routing redirect that names no destination", async () => {
    const res = await dispatchResult(
      {
        status: 307,
        resolvedHeaders: new Headers({ Location: "/redirect-dest" }),
      },
      new Request("https://app.example/redirect/a"),
      baseDeps({ assetStore: assetStoreServing({}) }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/redirect-dest");
  });

  it("lets a redirect status win over the page routing went on to resolve", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/about": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/about": "<h1>about</h1>" }),
    });

    const res = await dispatchResult(
      {
        status: 308,
        resolvedHeaders: new Headers({ Location: "/about/" }),
        resolvedPathname: "/about",
      },
      new Request("https://app.example/about"),
      deps,
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/about/");
  });

  it("answers a routing redirect expressed as a Refresh header", async () => {
    const res = await dispatchResult(
      {
        status: 307,
        resolvedHeaders: new Headers({ Refresh: "0;url=/redirect-dest" }),
      },
      new Request("https://app.example/redirect/a"),
      baseDeps({ assetStore: assetStoreServing({}) }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("refresh")).toBe("0;url=/redirect-dest");
  });

  it("serves the page when a headers() rule sets a location without a redirect status", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/about": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/about": "<h1>about</h1>" }),
    });

    const res = await dispatchResult(
      {
        resolvedHeaders: new Headers({ Location: "/elsewhere" }),
        resolvedPathname: "/about",
      },
      new Request("https://app.example/about"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>about</h1>");
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

// Next expresses a next.config `redirects()` entry as a beforeMiddleware rule
// carrying a Location header and a status and no destination — resolveRoutes
// reports those as a bare status, so the worker used to serve them as a 404
// wearing the right Location. Every rule here is a *user* rule: unmarked (no
// `priority`) under a manifest with no trailingSlash policy, which is what makes
// it reach isRoutingRedirect at all. Next's own internal trailing-slash rules
// never get this far — serve drops them (see trailing-slash-serve.test.ts).
describe("routing redirects that name no destination", () => {
  function redirectDeps(beforeMiddleware: Route[], files: Record<string, string> = {}) {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: Object.keys(files),
        routes: {
          beforeMiddleware,
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        },
        dispatch: Object.fromEntries(
          Object.keys(files).map((path) => [path, { kind: "static" as const }]),
        ),
      },
      assetStore: assetStoreServing(files),
    });
  }

  it("redirects a next.config rule with 307 rather than 404", async () => {
    const res = await serve(
      new Request("https://app.example/redirect/a", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/redirect/a(?:/)?$",
          headers: { Location: "/redirect-dest" },
          status: 307,
        },
      ]),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/redirect-dest");
  });

  it("answers a destination-less user rule that strips a trailing slash", async () => {
    const res = await serve(
      new Request("https://app.example/docs/", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/(.+?)/$",
          headers: { Location: "/$1" },
          // Next attaches this to the file rule so a next/link data request is
          // not bounced through a redirect.
          missing: [{ type: "header", key: "x-nextjs-data" }],
          status: 308,
        },
      ]),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/docs");
  });

  it("answers a destination-less user rule that adds one, over a page that resolves", async () => {
    const res = await serve(
      new Request("https://app.example/about", { redirect: "manual" }),
      redirectDeps(
        [
          {
            sourceRegex: "^/((?!\\.well-known(?:/.*)?)(?:[^/\\.]+/)*[^/\\.]+)$",
            headers: { Location: "/$1/" },
            missing: [{ type: "header", key: "x-nextjs-data" }],
            status: 308,
          },
        ],
        { "/about": "<h1>about</h1>" },
      ),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/about/");
  });
});

// A service worker registering at a scope broader than its own directory is
// rejected by the browser unless its script response carries
// Service-Worker-Allowed. Next sets that header in the same runtime branch this
// worker mirrors for cache-control, but it ALSO emits it as a build-time header
// rule (packages/next/src/build/index.ts pushes an internal rule for
// /_next/static/service-worker/:path*, which the adapter hands over as
// routing.beforeMiddleware and next-adapter.mts writes into the manifest
// verbatim). Routing already applies it, so serveStaticAsset must not set a
// second copy — this pins the header arriving from the manifest, and the
// cache-control the worker decides itself arriving alongside it.
describe("the service-worker chunk", () => {
  const swPath = "/_next/static/service-worker/sw.js";

  function swDeps(basePath: string) {
    const pathname = `${basePath}${swPath}`;
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath,
        pathnames: [pathname],
        routes: {
          // The rule as a real build emits it: internal, so priority is set and
          // the regex is used unmodified.
          beforeMiddleware: [
            {
              source: `${basePath}/_next/static/service-worker/:path*`,
              sourceRegex: `^${basePath}/_next/static/service-worker(?:/(.*))?(?:/)?$`,
              headers: { "Service-Worker-Allowed": basePath || "/" },
              priority: true,
            },
          ],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        },
        dispatch: { [pathname]: { kind: "static" } },
      },
      assetStore: assetStoreServing({ [pathname]: "self.addEventListener" }),
    });
  }

  it("carries Service-Worker-Allowed from the manifest and a revalidated policy", async () => {
    const res = await serve(new Request(`https://app.example${swPath}`), swDeps(""));

    expect(res.status).toBe(200);
    expect(res.headers.get("service-worker-allowed")).toBe("/");
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
    expect(res.headers.get("content-type")).toBe("text/javascript; charset=utf-8");
  });

  it("carries both under a basePath", async () => {
    const res = await serve(new Request(`https://app.example/docs${swPath}`), swDeps("/docs"));

    expect(res.status).toBe(200);
    expect(res.headers.get("service-worker-allowed")).toBe("/docs");
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
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

// A pages-router data request resolved by @next/routing's final dynamic-route
// table comes back with its /_next/data/<buildId>/… wrapper stripped, because
// that one branch never restores what it normalized away to match. Next decides
// a request is a data request from its URL alone, so the pathname the origin is
// invoked under is what these assert.
describe("data-request invocation pathname", () => {
  function lambdaDeps(
    manifest: Partial<RouteDeps["manifest"]> = {},
  ): { deps: RouteDeps; invoked: () => URL } {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/[...route]": { kind: "lambda", id: "fn", entryKey: "e" } },
        ...manifest,
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });
    return { deps, invoked: () => captured! };
  }

  it("forwards a _next/data request to the lambda under its data pathname", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("keeps the locale prefix on a data pathname", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/en/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/en/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/en/middleware/works.json");
  });

  it("maps the root invocation pathname back to index.json", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      { resolvedPathname: "/[...route]", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/_next/data/t/index.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/index.json");
  });

  it("drops a trailingSlash app's trailing slash from the data pathname", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works/" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("wraps the data pathname under the app's basePath", async () => {
    const { deps, invoked } = lambdaDeps({ basePath: "/docs" });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/docs/middleware/works" },
      },
      new Request("https://app.example/docs/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe(
      "/docs/_next/data/t/middleware/works.json",
    );
  });

  it("does not treat a lookalike prefix as the app's basePath", async () => {
    const { deps, invoked } = lambdaDeps({ basePath: "/docs" });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/docsy/works" },
      },
      new Request("https://app.example/docs/_next/data/t/docsy/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/docs/_next/data/t/docsy/works.json");
  });

  it("invokes an edge route with the data-wrapped pathname", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/[...route]": { kind: "edge", entryKey: "middleware_app/edge" },
        },
      },
      edge: async (_entryKey, request) => {
        captured = new URL(request.url);
        return new Response("{}", { status: 200 });
      },
    });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(captured!.pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("renders a prerendered route under the data pathname", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/[...route]": { kind: "prerender", id: "fn", config: {} },
        },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(captured!.pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("preserves the query string of a data request", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json?a=1"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/middleware/works.json");
    expect(invoked().search).toBe("?a=1");
  });

  it("leaves a document request's invocation pathname untouched", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/middleware/works"),
      deps,
    );

    expect(invoked().pathname).toBe("/middleware/works");
  });

  it("does not double-wrap an already-data invocation pathname", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/_next/data/t/index.json": {
            kind: "prerender",
            id: "fn",
            config: {},
          },
        },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/_next/data/t/index.json",
        invocationTarget: { pathname: "/_next/data/t/index.json" },
      },
      new Request("https://app.example/_next/data/t/index.json"),
      deps,
    );

    expect(captured!.pathname).toBe("/_next/data/t/index.json");
  });
});

// The alias the edge stamps beside x-ocel-cache for a build that asked for it
// (manifest vercelCacheAlias, from OCEL_E2E_VERCEL_CACHE_HEADER). Asserted
// through serve rather than dispatchResult: serve is the choke point every tier
// leaves through, which is the whole reason the stamp lives there.
describe("the x-vercel-cache alias", () => {
  const emptyRoutes = {
    beforeMiddleware: [],
    beforeFiles: [],
    afterFiles: [],
    dynamicRoutes: [],
    onMatch: [],
    fallback: [],
  };

  const isrPrefix = "prod/p/app/build";

  // A colo that always misses, so a prerender route is answered by the origin
  // and stamped MISS.
  const missingColo = () =>
    coloDeps({
      cache: {
        match: async () => undefined,
        put: async () => {},
      } as unknown as Cache,
      waitUntil: () => {},
    });

  function aliasDeps(vercelCacheAlias?: boolean): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/"],
        routes: emptyRoutes,
        dispatch: { "/": { kind: "prerender", id: "/", config: {} } },
        ...(vercelCacheAlias !== undefined && { vercelCacheAlias }),
      },
      functionUrls: { "/": "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: missingColo(),
    });
  }

  it("stamps the tier that answered under Vercel's name", async () => {
    const res = await serve(new Request("https://app.example/"), aliasDeps(true));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(res.headers.get("x-vercel-cache")).toBe("MISS");
    expect(await res.text()).toBe("rendered");
  });

  it("emits nothing for a build that did not ask for it", async () => {
    const res = await serve(new Request("https://app.example/"), aliasDeps());

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(res.headers.get("x-vercel-cache")).toBeNull();
  });

  // composePpr stamps its own status instead of going through withStatus, so the
  // composed-shell path is the one that proves the stamp is at the choke point
  // and not inside the cache tier.
  it("stamps a composed PPR shell, which never passes through withStatus", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/ppr"],
        routes: emptyRoutes,
        dispatch: {
          "/ppr": {
            kind: "prerender",
            id: "/ppr",
            config: {},
            fallback: { initialRevalidate: 60, initialExpiration: 3600 },
            pprChain: { headers: { "next-resume": "1" } },
          },
        },
        vercelCacheAlias: true,
      },
      functionUrls: { "/ppr": "https://fn.example.com" },
      fetch: (async () => new Response("[dynamic]", { status: 200 })) as unknown as typeof fetch,
      cache: missingColo(),
      interception: {
        config: { isrPrefix },
        now: () => 2_000,
        store: {
          async get(key: string) {
            if (key !== `${isrPrefix}/cache/ppr.cache.json`) return null;
            return {
              text: async () =>
                JSON.stringify({
                  lastModified: 1_000,
                  value: {
                    kind: "APP_PAGE",
                    html: "[shell]",
                    postponed: "POSTPONED",
                    status: 200,
                    headers: {},
                  },
                }),
            };
          },
        },
      },
    });

    const res = await serve(new Request("https://app.example/ppr"), deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(res.headers.get("x-vercel-cache")).toBe("PRERENDER");
  });
});
