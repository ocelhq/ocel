import { tagSnapshotKey } from "@framework/next-cache";
import { describe, expect, it } from "vitest";

import { dispatchResult, serve, type RouteDeps } from "../src/index";
import { refreshBackoffSeconds, sentinelUrl } from "../src/cache";
import { coloDeps } from "./cache-deps";
import { baseDeps } from "@framework/next-router/test-support/dispatch-scenario";

describe("dispatchResult", () => {
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
    const res = await dispatchPreview(bypassDeps("badcooki"), "badcookie");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("bypasses the cache when a real bypass cookie is present", async () => {
    const res = await dispatchPreview(bypassDeps("preview"), "preview=1");
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

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

  it("forwards a server action to a prerender origin with its own headers", async () => {
    let captured: Request | undefined;
    const res = await dispatchDraft(
      draftDeps((req) => (captured = req)),
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { cookie: "session=xyz", "next-action": "abc" },
        body: "{}",
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(captured?.headers.get("cookie")).toBe("session=xyz");
  });

  it("drops a client-forged next-resume from a server-action prerender forward", async () => {
    let captured: Request | undefined;
    await dispatchDraft(
      draftDeps((req) => (captured = req)),
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { "next-resume": "1", "next-action": "abc" },
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

  it("forwards the client's cookie to an uncacheable prerender origin", async () => {
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
      interception: {
        config: { isrPrefix: "prod/p/app/build" },
        now: () => 2_000,
        store: { async get() { return null; } },
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "2",
          cookie: "testCookie=initialValue",
        },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(captured?.headers.get("cookie")).toBe("testCookie=initialValue");
    expect(captured?.headers.get("purpose")).toBe("prefetch");
  });

  it("still strips the cookie from a cacheable prerender origin", async () => {
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

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "1",
          cookie: "testCookie=initialValue",
        },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(captured?.headers.get("cookie")).toBeNull();
  });

  const interceptionConfig = { isrPrefix: "prod/p/app/build" };

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

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
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
    expect(lambda).toBe(1);
  });

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

  function refreshOverStore(
    entries: Record<string, unknown>,
    landsDuringWait?: Record<string, unknown>,
    lambdaStatus = 200,
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
    const { deps, pending, puts } = refreshOverStore(staleBelow(), undefined, 429);

    await dispatchBlog(deps);
    await Promise.all(pending);

    const sentinelWrites = puts.filter(
      (put) => put.url === sentinelUrl("p1/web/d1:/blog"),
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

  it("promotes a stale entry below that is newer than the one being refreshed", async () => {
    const { deps, pending, puts, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>newer</html>"),
      200,
      1_500,
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
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

    const mirrored = puts.filter((put) => put.url.endsWith(".prefetch.rsc"));
    expect(mirrored.map((put) => put.entryModified).sort()).toEqual([
      String(1_000),
      String(1_000 + 61_000),
    ]);
  });

  it("renders when the fresher entry below cannot refill the colo's variant", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("[shell]", { postponed: "POSTPONED" }),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  it("skips the render when a variant with no colo entry is fresh below", async () => {
    const pprEntry = (html: string) => ({
      lastModified: html === "[shell]" ? 1_000 : 1_000 + 61_000,
      value: pageValue(html, {
        postponed: "POSTPONED",
        rscData: btoa(`${html}-rsc`),
      }),
    });
    const entries: Record<string, unknown> = { [entryKey("ppr")]: pprEntry("[shell]") };
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
        cache: coloHoldingSentinel("p1/web/d1:/blog"),
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
        cache: coloHoldingSentinel("p1/web/d1:/ppr"),
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
      interception: {
        config: interceptionConfig,
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
    expect(origins.map((req) => req.method)).toEqual(["POST"]);
  });

  it("refreshes the colo entry with the Lambda's fresh body after a stale R2 hit", async () => {
    const pending: Promise<unknown>[] = [];
    const stored = new Map<string, Response>();
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
    expect(await res.text()).toBe("<html>edge</html>");

    resolveLambda(
      new Response("fresh-lambda-body", {
        status: 200,
        headers: { "cache-control": "s-maxage=60" },
      }),
    );
    await Promise.all(pending);

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

  function pprDeps(opts: {
    resume: string;
    resumeHeaders?: Record<string, string>;
    entryPath?: string;
    entry: Record<string, unknown> | null;
    dispatch?: Record<string, unknown>;
    signed?: boolean;
  }): {
    deps: RouteDeps;
    resumeRequests: () => Request[];
    cachePuts: () => number;
    plainCalled: () => boolean;
    storeReads: () => number;
  } {
    const resumeRequests: Request[] = [];
    let puts = 0;
    let plainCalled = false;
    let reads = 0;
    const countingStore = (store: { get: (key: string) => Promise<unknown> }) => ({
      get: async (key: string) => {
        reads++;
        return store.get(key);
      },
    });
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
        store: countingStore(
          storeOf(
            opts.entry ? { [entryKey(opts.entryPath ?? "ppr")]: opts.entry } : {},
          ),
        ),
      },
    });
    return {
      deps,
      resumeRequests: () => resumeRequests,
      cachePuts: () => puts,
      plainCalled: () => plainCalled,
      storeReads: () => reads,
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
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("[rsc-shell]");
  });

  const pprShellWithFlight = {
    lastModified: 1_000,
    value: {
      kind: "APP_PAGE",
      html: "[shell]",
      rscData: btoa("[rsc-shell]"),
      postponed: "POSTPONED",
      status: 200,
      headers: {},
    },
  };

  it("answers a Flight navigation with the origin's render alone, never composed onto the shell", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    const res = await dispatchPpr(deps, { rsc: "1" });

    expect(await res.text()).toBe("[dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(resumeRequests()).toHaveLength(1);
    expect(resumeRequests()[0].method).toBe("GET");
  });

  it("does not read the shell out of the store for a Flight navigation", async () => {
    const { deps, storeReads } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    await (await dispatchPpr(deps, { rsc: "1" })).text();

    expect(storeReads()).toBe(0);
  });

  it("still composes the document request against the same entry", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(resumeRequests()[0].method).toBe("POST");
  });

  it("treats a malformed RSC header as a document request, matching the origin", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    const res = await dispatchPpr(deps, { rsc: "2" });

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()[0].method).toBe("POST");
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

});

describe("a Server Action's invalidation reaching the colo it travelled through", () => {
  const cfg = { isrPrefix: "prod/p/app/build" };

  function scenario(announce = "x-action-revalidated") {
    let now = 10_000;
    let lambdaCalls = 0;
    let actionRevalidates = true;
    let pagesAnnounce = false;
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
          return new Response("action", {
            status: 200,
            headers: actionRevalidates ? { [announce]: "1" } : {},
          });
        }
        return new Response("page", {
          status: 200,
          headers: {
            "cache-control": "s-maxage=31536000",
            "x-nextjs-cache": "HIT",
            ...(pagesAnnounce ? { [announce]: "1" } : {}),
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
      announceOnPages: () => {
        pagesAnnounce = true;
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

  it("renders the invalidated entry at the origin on the next request", async () => {
    const s = scenario();

    await s.get();
    await s.settle();
    expect(s.lambdaCalls()).toBe(1);

    s.invalidate(20_000);
    s.advanceTo(15_000);
    const action = await s.runAction();
    expect(action.headers.get("x-action-revalidated")).toBe("1");
    await s.settle();
    expect(s.lambdaCalls()).toBe(2);

    s.advanceTo(30_000);
    const after = await s.get();

    expect(after.headers.get("x-ocel-cache")).toBe("MISS");
    await s.settle();
    expect(s.lambdaCalls()).toBe(3);
  });

  it("takes the origin's own announcement, and keeps it off the client's response", async () => {
    const s = scenario("x-ocel-revalidated");

    await s.get();
    await s.settle();

    s.invalidate(20_000);
    s.advanceTo(15_000);
    const announced = await s.runAction();
    expect(announced.headers.has("x-ocel-revalidated")).toBe(false);
    await s.settle();

    s.advanceTo(30_000);
    const after = await s.get();

    expect(after.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("keeps the origin's announcement out of the colo entry it stores", async () => {
    const s = scenario("x-ocel-revalidated");
    s.announceOnPages();

    const first = await s.get();
    expect(first.headers.has("x-ocel-revalidated")).toBe(false);
    await s.settle();

    s.advanceTo(15_000);
    s.invalidate(12_000);
    const hit = await s.get();
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(hit.headers.has("x-ocel-revalidated")).toBe(false);
    await s.settle();

    s.advanceTo(30_000);
    const again = await s.get();
    expect(again.headers.get("x-ocel-cache")).toBe("HIT");
  });

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

describe("an afterFiles rewrite shadowed by a dynamic route", () => {
  it("uses the exact page's own prerender config after the swap, not the shadowing template's", async () => {
    let captured: { host: string; headers: Headers } | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/ssg/hello", "/ssg/[slug]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [
            { sourceRegex: "^/to-ssg(?:/)?$", destination: "/ssg/hello" },
          ],
          dynamicRoutes: [
            {
              sourceRegex: "^/ssg/(?<nxtPslug>[^/]+?)(?:/)?$",
              destination: "/ssg/[slug]?nxtPslug=$nxtPslug",
            },
          ],
          onMatch: [],
          fallback: [],
        },
        dispatch: {
          "/ssg/hello": {
            kind: "prerender",
            id: "hello-fn",
            config: { allowHeader: ["x-marker-hello"], allowQuery: ["keep-hello"] },
          },
          "/ssg/[slug]": {
            kind: "prerender",
            id: "slug-fn",
            config: { allowHeader: ["x-marker-slug"], allowQuery: ["keep-slug"] },
          },
        },
      },
      functionUrls: {
        "hello-fn": "https://hello.example.com",
        "slug-fn": "https://slug.example.com",
      },
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: () => {},
      }),
      fetch: (async (req: Request) => {
        captured = { host: new URL(req.url).host, headers: req.headers };
        return new Response("rendered", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await serve(
      new Request("https://app.example/to-ssg", {
        headers: { "x-marker-hello": "1", "x-marker-slug": "1" },
      }),
      deps,
    );

    expect(res.headers.get("x-matched-path")).toBe("/ssg/hello");
    expect(captured?.host).toBe("hello.example.com");
    expect(captured?.headers.get("x-marker-hello")).toBe("1");
    expect(captured?.headers.has("x-marker-slug")).toBe(false);
  });
});

describe("the colo cache key under a config rewrite", () => {
  it("keys two source URLs that rewrite to the same destination separately", async () => {
    const seen: string[] = [];
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/bar"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [
            { sourceRegex: "^/foo1$", destination: "/bar" },
            { sourceRegex: "^/foo2$", destination: "/bar" },
          ],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/bar": {
            kind: "prerender",
            id: "bar",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { bar: "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (req: Request) => {
            seen.push(req.url);
            return undefined;
          },
          put: async (req: Request) => {
            seen.push(req.url);
          },
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => pending.push(p),
      }),
    });

    await serve(new Request("https://app.example/foo1"), deps);
    await serve(new Request("https://app.example/foo2"), deps);
    await Promise.all(pending);

    expect(seen.some((k) => k.endsWith("/foo1"))).toBe(true);
    expect(seen.some((k) => k.endsWith("/foo2"))).toBe(true);
    expect(seen.some((k) => k.endsWith("/bar"))).toBe(false);
  });

  it("keys two concrete paths of the same dynamic route separately, not by the route pattern", async () => {
    const seen: string[] = [];
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/blog/[slug]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/blog/(?<nxtPslug>[^/]+?)(?:/)?$",
              destination: "/blog/[slug]?nxtPslug=$nxtPslug",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/blog/[slug]": {
            kind: "prerender",
            id: "blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { blog: "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (req: Request) => {
            seen.push(req.url);
            return undefined;
          },
          put: async (req: Request) => {
            seen.push(req.url);
          },
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => pending.push(p),
      }),
    });

    await serve(new Request("https://app.example/blog/post-1"), deps);
    await serve(new Request("https://app.example/blog/post-2"), deps);
    await Promise.all(pending);

    expect(seen.some((k) => k.endsWith("/blog/post-1"))).toBe(true);
    expect(seen.some((k) => k.endsWith("/blog/post-2"))).toBe(true);
    expect(seen.some((k) => k.includes("[slug]"))).toBe(false);
  });
});

describe("a fallback path of a dynamic route's ISR revalidation", () => {
  it("reads the store at the concrete requested path and enqueues that same concrete path, not the route pattern", async () => {
    const isrPrefix = "prod/p/app/build";
    const storeReads: string[] = [];
    const sent: unknown[] = [];
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/posts/[id]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/posts/(?<nxtPid>[^/]+?)(?:/)?$",
              destination: "/posts/[id]?nxtPid=$nxtPid",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/posts/[id]": {
            kind: "prerender",
            id: "posts",
            entryKey: "app/posts/[id]/page",
            config: { bypassToken: "TOKEN" },
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { posts: "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: { match: async () => undefined, put: async () => {} } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => pending.push(p),
        enqueueRevalidation: async (message) => {
          sent.push(message);
          return true;
        },
      }),
      interception: {
        config: { isrPrefix },
        now: () => 1_000 + 61_000,
        store: {
          async get(key: string) {
            storeReads.push(key);
            if (key !== `${isrPrefix}/cache/posts/7.cache.json`) return null;
            return {
              text: async () =>
                JSON.stringify({
                  lastModified: 1_000,
                  value: {
                    kind: "APP_PAGE",
                    html: "<html>post-7</html>",
                    status: 200,
                    headers: {},
                  },
                }),
            };
          },
        },
      },
    });

    const res = await serve(new Request("https://app.example/posts/7"), deps);
    await Promise.all(pending);

    expect(await res.text()).toBe("<html>post-7</html>");
    expect(storeReads).toContain("prod/p/app/build/cache/posts/7.cache.json");
    expect(storeReads).not.toContain("prod/p/app/build/cache/posts/[id].cache.json");
    expect(sent).toEqual([
      expect.objectContaining({ routePath: "/posts/7" }),
    ]);
  });
});

describe("a generated interception rewrite's prerender key", () => {
  const isrPrefix = "prod/p/app/build";
  const entryKey = "(.)test-nested";

  function interceptionDeps(overrides: { lambdaBody?: string } = {}) {
    let lambda = 0;
    return {
      deps: baseDeps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/test-nested", "/(.)test-nested"],
          routes: {
            beforeMiddleware: [],
            beforeFiles: [
              {
                sourceRegex: "^/test-nested$",
                destination: "/(.)test-nested",
                has: [{ type: "header", key: "next-url" }],
              },
            ],
            afterFiles: [],
            dynamicRoutes: [],
            onMatch: [],
            fallback: [],
          } as unknown as RouteDeps["manifest"]["routes"],
          dispatch: {
            "/(.)test-nested": {
              kind: "prerender",
              id: "test-nested",
              config: {},
              fallback: { initialRevalidate: 60 },
            },
          },
        },
        functionUrls: { "test-nested": "https://fn.example.com" },
        fetch: (async () => {
          lambda++;
          return new Response(overrides.lambdaBody ?? "from-lambda", {
            status: 200,
            headers: { "cache-control": "s-maxage=60" },
          });
        }) as unknown as typeof fetch,
        cache: coloDeps({
          cache: {
            match: async () => undefined,
            put: async () => {},
          } as unknown as Cache,
          waitUntil: () => {},
        }),
        interception: {
          config: { isrPrefix },
          now: () => 2_000,
          store: {
            async get(key: string) {
              if (key !== `${isrPrefix}/cache/${entryKey}.cache.json`) return null;
              return {
                text: async () =>
                  JSON.stringify({
                    lastModified: 1_000,
                    value: {
                      kind: "APP_PAGE",
                      html: "<html>intercepted</html>",
                      status: 200,
                      headers: {},
                      segmentData: { "/children": btoa("INTERCEPTED-SEGMENT") },
                      segmentHeaders: { "content-type": "text/x-component" },
                    },
                  }),
              };
            },
          },
        },
      }),
      lambdaCalls: () => lambda,
    };
  }

  it("resolves a segment-prefetch request's ISR key to the rewritten path, not the requested one", async () => {
    const { deps, lambdaCalls } = interceptionDeps();

    const res = await serve(
      new Request("https://app.example/test-nested", {
        headers: {
          RSC: "1",
          "next-url": "/",
          "next-router-segment-prefetch": "/children",
        },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("INTERCEPTED-SEGMENT");
    expect(lambdaCalls()).toBe(0);
  });

  it("still forwards the requested (source) path to the origin, unaffected by the ISR key fix", async () => {
    let capturedPath: string | undefined;
    const { deps } = interceptionDeps();
    deps.fetch = (async (req: Request) => {
      capturedPath = new URL(req.url).pathname;
      return new Response("miss", { status: 404 });
    }) as unknown as typeof fetch;
    deps.interception!.store = { async get() { return null; } };

    await serve(
      new Request("https://app.example/test-nested", {
        headers: {
          RSC: "1",
          "next-url": "/",
          "next-router-segment-prefetch": "/children",
        },
      }),
      deps,
    );

    expect(capturedPath).toBe("/test-nested");
  });
});

describe("an origin that cannot answer a segment prefetch", () => {
  function scenario(originHeaders: Record<string, string>) {
    let lambdaCalls = 0;
    const colo = new Map<string, Response>();
    const pending: Promise<unknown>[] = [];
    const settle = async () => {
      while (pending.length) await pending.shift();
      await new Promise((resolve) => setTimeout(resolve, 0));
    };

    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/settings"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/settings": {
            kind: "prerender",
            id: "settings",
            config: { renderingMode: "PARTIALLY_STATIC" },
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { settings: "https://fn.example.com" },
      fetch: (async () => {
        lambdaCalls++;
        return new Response("SHELL-OR-SEGMENT", {
          status: 200,
          headers: { "cache-control": "s-maxage=60", ...originHeaders },
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
        now: () => 1_000,
      }),
    });

    const prefetch = () =>
      serve(
        new Request("https://app.example/settings", {
          headers: {
            RSC: "1",
            "next-router-prefetch": "1",
            "next-router-segment-prefetch": "/$d$team/$d$project/settings",
          },
        }),
        deps,
      );

    return { prefetch, settle, colo, lambda: () => lambdaCalls };
  }

  it("answers a 204 miss instead of handing the client a postponed shell", async () => {
    const { prefetch, settle } = scenario({ "x-nextjs-postponed": "1" });

    const res = await prefetch();
    await settle();

    expect(res.status).toBe(204);
    expect(await res.text()).toBe("");
  });

  it("stores nothing under the segment key, so the next prefetch is not latched to the shell", async () => {
    const { prefetch, settle, colo, lambda } = scenario({
      "x-nextjs-postponed": "1",
    });

    await (await prefetch()).text();
    await settle();
    const second = await prefetch();
    await settle();

    expect([...colo.keys()]).toEqual([]);
    expect(second.status).toBe(204);
    expect(lambda()).toBe(2);
  });

  it("still caches and serves a real segment payload", async () => {
    const { prefetch, settle, colo, lambda } = scenario({
      "x-nextjs-postponed": "2",
    });

    const first = await prefetch();
    expect(await first.text()).toBe("SHELL-OR-SEGMENT");
    await settle();

    const second = await prefetch();

    expect([...colo.keys()]).toEqual([
      "https://cache.ocel/p1/web/d1/settings.segments/%2F%24d%24team%2F%24d%24project%2Fsettings.segment.rsc",
    ]);
    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await second.text()).toBe("SHELL-OR-SEGMENT");
    expect(lambda()).toBe(1);
  });
});

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
