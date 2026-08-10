import type { Route } from "@next/routing";
import { describe, expect, it } from "vitest";

import { serve, type RouteDeps } from "../src/index";
import type { AssetBucket } from "../src/assets";
import { coloDeps } from "./cache-deps";

function assetStoreServing(
  files: Record<string, string>,
  probes?: string[],
  basePath = "",
): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      probes?.push(key);
      const body = files[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: "",
    basePath,
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

// Next's own internal trailing-slash redirects, which it unshifts ahead of every
// other route, as the build emits them into beforeMiddleware: destination-less
// rules carrying a Location, a 308, and the `priority` flag build-complete.ts
// maps `internal: true` onto. basePath is baked into the source, because the
// build compiles these from the routes manifest, whose regex already carries it
// (and internal rules skip modifyRouteRegex, so the source is used as-is).
//
// Included in every scenario below so the tests also prove serve drops them
// rather than 308ing the canonical form it just normalized straight back at the
// client.
function internalRedirects(trailingSlash: boolean, basePath = ""): unknown[] {
  if (trailingSlash) {
    return [
      // basePath: false on this one, so its source is the bare basePath.
      ...(basePath
        ? [
            {
              sourceRegex: `^${basePath}$`,
              headers: { Location: `${basePath}/` },
              status: 308,
              priority: true,
            },
          ]
        : []),
      {
        sourceRegex: `^${basePath}/((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/]+\\.\\w+)/$`,
        headers: { Location: `${basePath}/$1` },
        missing: [{ type: "header", key: "x-nextjs-data" }],
        status: 308,
        priority: true,
      },
      {
        sourceRegex: `^${basePath}/((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/\\.]+)$`,
        headers: { Location: `${basePath}/$1/` },
        status: 308,
        priority: true,
      },
    ];
  }
  return [
    ...(basePath
      ? [
          {
            sourceRegex: `^${basePath}/$`,
            headers: { Location: basePath },
            status: 308,
            priority: true,
          },
        ]
      : []),
    {
      sourceRegex: `^${basePath}/(.+?)/$`,
      headers: { Location: `${basePath}/$1` },
      status: 308,
      priority: true,
    },
  ];
}

// The two rules withoutInternalRedirects must NOT drop, riding alongside the
// internal ones in every scenario:
//
//   * Next's own Service-Worker-Allowed header rule, which the build marks
//     `priority` exactly as it marks the redirects but which carries no status —
//     the half of the predicate that keeps header rules alive.
//   * an ordinary next.config `redirects()` entry, which can never be marked
//     (checkCustomRoutes rejects `internal` from user config) and so must keep
//     reaching isRoutingRedirect.
const SERVICE_WORKER_PATH = "/_next/static/service-worker/sw.js";
const USER_REDIRECT_FROM = "/old.txt";

function survivingRoutes(basePath = ""): unknown[] {
  return [
    {
      sourceRegex: `^${basePath}/_next/static/service-worker/(.*)$`,
      headers: { "Service-Worker-Allowed": basePath || "/" },
      priority: true,
    },
    {
      sourceRegex: `^${basePath}${USER_REDIRECT_FROM}(?:/)?$`,
      headers: { Location: `${basePath}/a/` },
      status: 308,
    },
  ];
}

function manifestRoutes(
  trailingSlash: boolean,
  basePath = "",
  headerRoutes: unknown[] = [],
): Route[] {
  return [
    // Where the build puts next.config `headers()`: ahead of the redirects, in
    // the same list, so Next applies them to the redirect response too.
    ...headerRoutes,
    ...internalRedirects(trailingSlash, basePath),
    ...survivingRoutes(basePath),
  ] as Route[];
}

interface Scenario {
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  basePath?: string;
  // Served paths, by the routing-form pathname the build keyed them under.
  pages?: string[];
  files?: Record<string, string>;
  edge?: RouteDeps["edge"];
  middleware?: { entryKey: string; matchers?: { sourceRegex: string }[] };
  // Overrides the all-static dispatch table `pages` implies, for the lambda and
  // prerender arms.
  dispatch?: RouteDeps["manifest"]["dispatch"];
  functionUrls?: RouteDeps["functionUrls"];
  fetch?: RouteDeps["fetch"];
  cache?: RouteDeps["cache"];
  // Every key serveStaticAsset asked the store for, in order.
  probes?: string[];
  // next.config `headers()` rules, as the build emits them into beforeMiddleware.
  headerRoutes?: unknown[];
}

function deps(scenario: Scenario): RouteDeps {
  const basePath = scenario.basePath ?? "";
  const pages = scenario.pages ?? [];
  return {
    manifest: {
      buildId: "t",
      basePath,
      trailingSlash: scenario.trailingSlash,
      skipTrailingSlashRedirect: scenario.skipTrailingSlashRedirect,
      pathnames: pages,
      routes: {
        beforeMiddleware: manifestRoutes(
          !!scenario.trailingSlash,
          basePath,
          scenario.headerRoutes,
        ),
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch:
        scenario.dispatch ??
        Object.fromEntries(pages.map((path) => [path, { kind: "static" as const }])),
      middleware: scenario.middleware,
    },
    functionUrls: scenario.functionUrls ?? {},
    slug: "p1",
    app: "web",
    assetStore: assetStoreServing(
      { "/404.html": "not found", ...(scenario.files ?? {}) },
      scenario.probes,
      basePath,
    ),
    edge: scenario.edge,
    fetch: scenario.fetch,
    cache: scenario.cache,
  };
}

function get(path: string, headers: Record<string, string> = {}) {
  return new Request(`https://app.example${path}`, { redirect: "manual", headers });
}

describe("trailingSlash: true", () => {
  const scenario: Scenario = {
    trailingSlash: true,
    pages: ["/a", "/next.svg", "/_next/data/t/a.json"],
    files: {
      "/a.html": "<h1>a</h1>",
      "/next.svg": "<svg/>",
      "/_next/data/t/a.json": '{"pageProps":{}}',
      "/_next/static/chunks/a.js": "chunk",
      "/.well-known/acme": "token",
    },
  };

  it("leaves the root alone", async () => {
    const res = await serve(
      get("/"),
      // The build names the root document index.html, not /.html — the same
      // name normalizePagePath gives every Pages Router root.
      deps({ ...scenario, pages: ["/"], files: { "/index.html": "home" } }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("home");
  });

  it.each([
    ["/a", "/a/"],
    ["/blog/post", "/blog/post/"],
    ["/next.svg/", "/next.svg"],
  ])("308s %s to %s", async (from, to) => {
    const res = await serve(get(from), deps(scenario));
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe(to);
  });

  it("carries the query string through the 308", async () => {
    const res = await serve(get("/a?q=1"), deps(scenario));
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a/?q=1");
  });

  it("serves the canonical slashed page from the slash-free build pathname", async () => {
    const res = await serve(get("/a/"), deps(scenario));
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>a</h1>");
    expect(res.headers.get("x-matched-path")).toBe("/a");
  });

  it.each(["/next.svg", "/_next/static/chunks/a.js", "/.well-known/acme"])(
    "does not redirect %s",
    async (path) => {
      const res = await serve(get(path), deps(scenario));
      expect(res.status).toBe(200);
    },
  );

  it("does not redirect a data request", async () => {
    const res = await serve(
      get("/_next/data/t/a.json", { "x-nextjs-data": "1" }),
      deps(scenario),
    );
    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/_next/data/t/a.json");
  });
});

describe("trailingSlash: true, basePath: /docs", () => {
  const scenario: Scenario = {
    trailingSlash: true,
    basePath: "/docs",
    pages: ["/docs", "/docs/hello"],
    // The build stores a basePath root at "<basePath>/index.html", never
    // "<basePath>.html" — the same normalizePagePath-derived name a bare "/"
    // root gets (see the "leaves the root alone" tests above).
    files: { "/docs/index.html": "docs root", "/docs/hello.html": "hello" },
  };

  it("308s the bare basePath to the slashed one", async () => {
    const res = await serve(get("/docs"), deps(scenario));
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/docs/");
  });

  it("serves the slashed basePath root", async () => {
    const res = await serve(get("/docs/"), deps(scenario));
    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/docs");
    expect(await res.text()).toBe("docs root");
  });

  it("serves a slashed page under basePath", async () => {
    const res = await serve(get("/docs/hello/"), deps(scenario));
    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/docs/hello");
    expect(await res.text()).toBe("hello");
  });
});

describe("trailingSlash: false", () => {
  const scenario: Scenario = {
    pages: ["/a", "/_next/data/t/a.json"],
    files: {
      "/a.html": "<h1>a</h1>",
      "/_next/data/t/a.json": '{"pageProps":{}}',
      "/_next/static/chunks/a.js": "chunk",
    },
  };

  it("serves the slash-free page", async () => {
    const res = await serve(get("/a"), deps(scenario));
    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/a");
  });

  it("308s the slashed page to the slash-free one", async () => {
    const res = await serve(get("/a/"), deps(scenario));
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a");
  });

  it("leaves the root alone", async () => {
    const res = await serve(
      get("/"),
      // index.html, not /.html — see the trailingSlash: true version above.
      deps({ ...scenario, pages: ["/"], files: { "/index.html": "home" } }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("home");
  });

  it("strips the slash off a build asset too — the rule is generic", async () => {
    const res = await serve(get("/_next/static/chunks/a.js/"), deps(scenario));
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/_next/static/chunks/a.js");
  });

  it("does not redirect a data request", async () => {
    const res = await serve(
      get("/_next/data/t/a.json", { "x-nextjs-data": "1" }),
      deps(scenario),
    );
    expect(res.status).toBe(200);
  });
});

// Constraint: skipTrailingSlashRedirect suppresses the 308 and nothing else. The
// routing-form strip stays unconditional, or the canonical `/a/` still 404s.
describe("skipTrailingSlashRedirect", () => {
  for (const trailingSlash of [true, false]) {
    describe(`trailingSlash: ${trailingSlash}`, () => {
      const scenario: Scenario = {
        trailingSlash,
        skipTrailingSlashRedirect: true,
        pages: ["/a"],
        files: { "/a.html": "<h1>a</h1>" },
      };

      it.each(["/a", "/a/"])("serves %s without a redirect", async (path) => {
        const res = await serve(get(path), deps(scenario));
        expect(res.status).toBe(200);
        expect(await res.text()).toBe("<h1>a</h1>");
        expect(res.headers.get("x-matched-path")).toBe("/a");
      });
    });
  }
});

// Constraint: the 308 precedes middleware invocation and body buffering.
describe("a request about to be redirected", () => {
  function counting() {
    let calls = 0;
    const scenario: Scenario = {
      trailingSlash: true,
      pages: ["/a"],
      files: { "/a.html": "<h1>a</h1>" },
      middleware: { entryKey: "mw", matchers: [{ sourceRegex: "^/.*$" }] },
      edge: async () => {
        calls += 1;
        return new Response("from middleware", { status: 200 });
      },
    };
    return { scenario, calls: () => calls };
  }

  it("never invokes middleware", async () => {
    const { scenario, calls } = counting();
    const res = await serve(get("/a"), deps(scenario));
    expect(res.status).toBe(308);
    expect(calls()).toBe(0);
  });

  it("still invokes middleware for the canonical form", async () => {
    const { scenario, calls } = counting();
    const res = await serve(get("/a/"), deps(scenario));
    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from middleware");
    expect(calls()).toBe(1);
  });

  it("never reads the body of a redirected POST", async () => {
    const request = new Request("https://app.example/a", {
      method: "POST",
      body: "payload",
      redirect: "manual",
    });
    const res = await serve(request, deps({ trailingSlash: true, pages: ["/a"] }));
    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a/");
    expect(request.bodyUsed).toBe(false);
  });
});

// Next compiles basePath into the source of every internal rule, so an
// off-basePath path matches none of them and Next 404s it as it stands. A
// redirect here would regress a correct 404 and tell a prober the app's basePath.
describe("basePath: /docs, paths that are not under it", () => {
  for (const trailingSlash of [true, false]) {
    describe(`trailingSlash: ${trailingSlash}`, () => {
      const scenario: Scenario = {
        trailingSlash,
        basePath: "/docs",
        pages: ["/docs/hello"],
        files: { "/docs/hello.html": "hello" },
      };

      it.each(["/", "/favicon.ico", "/wp-admin", "/foo", "/docsy/page", "/docsy/page/"])(
        "404s %s without a redirect",
        async (path) => {
          const res = await serve(get(path), deps(scenario));
          expect(res.status).toBe(404);
          expect(await res.text()).toBe("not found");
        },
      );
    });
  }
});

// The negative side of withoutInternalRedirects: it must drop Next's internal
// trailing-slash redirects and nothing else.
describe("routes withoutInternalRedirects keeps", () => {
  for (const trailingSlash of [true, false]) {
    describe(`trailingSlash: ${trailingSlash}`, () => {
      const scenario: Scenario = {
        trailingSlash,
        pages: ["/a", SERVICE_WORKER_PATH],
        files: { "/a.html": "<h1>a</h1>", [SERVICE_WORKER_PATH]: "self.skipWaiting()" },
      };

      // Marked `priority` like the redirects, but it carries no status.
      it("Next's own priority-flagged header rule", async () => {
        const res = await serve(get(SERVICE_WORKER_PATH), deps(scenario));
        expect(res.status).toBe(200);
        expect(res.headers.get("service-worker-allowed")).toBe("/");
      });

      it("an unmarked next.config redirect", async () => {
        const res = await serve(get(USER_REDIRECT_FROM), deps(scenario));
        expect(res.status).toBe(308);
        expect(res.headers.get("location")).toBe("/a/");
      });
    });
  }
});

// ---------------------------------------------------------------------------
// What the response is keyed by, once the seam has run.
//
// ocelhq-xlj's field face was a 404 on the canonical URL, and the merge note on
// it argued the real defect was asset keying: a kind:static dispatch is read
// from R2 by the REQUEST pathname (index.ts's static arm hands serveStaticAsset
// `url`, not `result.resolvedPathname`), so `/a/` resolving to `/a` would still
// read `<prefix>/a/.html` and miss. These pin that for a DIRECTLY requested
// path: `serve` rebuilt the request on the routing form, so `url.pathname` IS
// the resolved path by the time dispatch runs and every downstream key — the R2
// object, the colo cache entry, the origin forward — is the slash-free one.
//
// The other face of that keying bug is untouched here and stays open
// (ocelhq-t2qx / ocelhq-iud): a kind:static target reached through a rewrite is
// still read by the SOURCE page's path, because there the request pathname and
// the resolved pathname genuinely differ. Nothing below routes through a
// rewrite, deliberately.
describe("the resolved path is what keys the response", () => {
  const ENTRY_HEADER = "x-ocel-entry";

  describe("a static prerender, read from R2", () => {
    it.each([false, true])(
      "serves /a/ from /a.html under trailingSlash: true (skipTrailingSlashRedirect: %s)",
      async (skipTrailingSlashRedirect) => {
        const probes: string[] = [];
        const res = await serve(
          get("/a/"),
          deps({
            trailingSlash: true,
            skipTrailingSlashRedirect,
            pages: ["/a"],
            files: { "/a.html": "<h1>a</h1>" },
            probes,
          }),
        );

        expect(res.status).toBe(200);
        expect(await res.text()).toBe("<h1>a</h1>");
        expect(res.headers.get("x-matched-path")).toBe("/a");
        expect(probes).toContain("/a.html");
        expect(probes.some((key) => key.includes("/a/"))).toBe(false);
      },
    );

    it("under trailingSlash: false, /a/ 308s and /a serves", async () => {
      const scenario: Scenario = {
        pages: ["/a"],
        files: { "/a.html": "<h1>a</h1>" },
      };

      const slashed = await serve(get("/a/"), deps(scenario));
      expect(slashed.status).toBe(308);
      expect(slashed.headers.get("location")).toBe("/a");

      const probes: string[] = [];
      const bare = await serve(get("/a"), deps({ ...scenario, probes }));
      expect(bare.status).toBe(200);
      expect(await bare.text()).toBe("<h1>a</h1>");
      expect(probes).toContain("/a.html");
    });

    // Both of these 404'd in the field: the basePath root and a page under it.
    it("serves the basePath root and a page under it from their own objects", async () => {
      const probes: string[] = [];
      const scenario: Scenario = {
        trailingSlash: true,
        basePath: "/docs",
        pages: ["/docs", "/docs/hello"],
        files: { "/docs/index.html": "docs root", "/docs/hello.html": "hello" },
        probes,
      };

      const root = await serve(get("/docs/"), deps(scenario));
      expect(root.status).toBe(200);
      expect(await root.text()).toBe("docs root");
      expect(probes).toContain("/docs/index.html");

      const page = await serve(get("/docs/hello/"), deps(scenario));
      expect(page.status).toBe(200);
      expect(await page.text()).toBe("hello");
      expect(probes).toContain("/docs/hello.html");
      expect(probes.some((key) => key.endsWith("/.html"))).toBe(false);
    });
  });

  describe("a lambda route, forwarded to its Function URL", () => {
    function lambdaScenario(
      overrides: Partial<Scenario> & { route: string },
    ): { scenario: Scenario; forwarded: () => Request | undefined } {
      let captured: Request | undefined;
      const { route, ...rest } = overrides;
      return {
        forwarded: () => captured,
        scenario: {
          pages: [route],
          dispatch: { [route]: { kind: "lambda", id: "fn", entryKey: "page:ssr" } },
          functionUrls: { fn: "https://fn.example.com" },
          fetch: (async (input: Request) => {
            captured = input;
            return new Response("ssr", { status: 200 });
          }) as unknown as typeof fetch,
          ...rest,
        },
      };
    }

    it("forwards /ssr/ on the slash-free path under trailingSlash: true", async () => {
      const { scenario, forwarded } = lambdaScenario({
        route: "/ssr",
        trailingSlash: true,
      });

      const res = await serve(get("/ssr/?q=1"), deps(scenario));

      expect(res.status).toBe(200);
      expect(await res.text()).toBe("ssr");
      const url = new URL(forwarded()!.url);
      expect(url.host).toBe("fn.example.com");
      expect(url.pathname).toBe("/ssr");
      expect(url.search).toBe("?q=1");
      expect(forwarded()!.headers.get(ENTRY_HEADER)).toBe("page:ssr");
    });

    it("308s /ssr to /ssr/ under trailingSlash: true without forwarding", async () => {
      const { scenario, forwarded } = lambdaScenario({
        route: "/ssr",
        trailingSlash: true,
      });

      const res = await serve(get("/ssr"), deps(scenario));

      expect(res.status).toBe(308);
      expect(res.headers.get("location")).toBe("/ssr/");
      expect(forwarded()).toBeUndefined();
    });

    it("forwards /ssr on the same path under trailingSlash: false", async () => {
      const { scenario, forwarded } = lambdaScenario({ route: "/ssr" });

      const res = await serve(get("/ssr"), deps(scenario));

      expect(res.status).toBe(200);
      expect(new URL(forwarded()!.url).pathname).toBe("/ssr");

      const slashed = await serve(get("/ssr/"), deps(scenario));
      expect(slashed.status).toBe(308);
      expect(slashed.headers.get("location")).toBe("/ssr");
    });

    // The other path observed 404ing in the field.
    it("forwards /docs/ssr/ as /docs/ssr under a basePath", async () => {
      const { scenario, forwarded } = lambdaScenario({
        route: "/docs/ssr",
        trailingSlash: true,
        basePath: "/docs",
      });

      const res = await serve(get("/docs/ssr/"), deps(scenario));

      expect(res.status).toBe(200);
      expect(new URL(forwarded()!.url).pathname).toBe("/docs/ssr");
      expect(forwarded()!.headers.get(ENTRY_HEADER)).toBe("page:ssr");
    });
  });

  describe("a prerender route, keyed in the colo cache", () => {
    function coloScenario(trailingSlash: boolean, skip = false) {
      const stored = new Map<string, Response>();
      const pending: Promise<unknown>[] = [];
      let renders = 0;
      const scenario: Scenario = {
        trailingSlash,
        skipTrailingSlashRedirect: skip,
        pages: ["/p"],
        dispatch: { "/p": { kind: "prerender", id: "fn", config: {} } },
        functionUrls: { fn: "https://fn.example.com" },
        fetch: (async () => {
          renders += 1;
          return new Response("prerendered", {
            status: 200,
            headers: { "cache-control": "s-maxage=60" },
          });
        }) as unknown as typeof fetch,
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
      };
      const settle = () => Promise.all(pending.splice(0));
      return { scenario, settle, renders: () => renders, keys: () => [...stored.keys()] };
    }

    it("gives the canonical and the slash-free form one key under trailingSlash: true", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true);
      const d = deps(scenario);

      const first = await serve(get("/p/"), d);
      expect(first.status).toBe(200);
      expect(first.headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      // The client's next hop for /p is the 308's target, which must land on the
      // entry the first request wrote.
      const redirect = await serve(get("/p"), d);
      expect(redirect.status).toBe(308);
      expect(redirect.headers.get("location")).toBe("/p/");

      const follow = await serve(get("/p/"), d);
      expect(follow.headers.get("x-ocel-cache")).toBe("HIT");
      expect(await follow.text()).toBe("prerendered");
      expect(renders()).toBe(1);
      expect(keys()).toEqual(["https://cache.ocel/t/p"]);
    });

    // With the 308 suppressed both forms are served, so the two requests reach
    // the cache key directly rather than through a redirect — the sharpest test
    // that cacheKey does not fork on the slash.
    it("gives both served forms one key under skipTrailingSlashRedirect", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true, true);
      const d = deps(scenario);

      const first = await serve(get("/p/"), d);
      expect(first.headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      const second = await serve(get("/p"), d);
      expect(second.headers.get("x-ocel-cache")).toBe("HIT");
      expect(await second.text()).toBe("prerendered");
      expect(renders()).toBe(1);
      expect(keys()).toEqual(["https://cache.ocel/t/p"]);
    });
  });

  // The fallthrough arm: no resolvedPathname at all, so the asset store is the
  // only thing that can answer. It must still be probed on the routing form.
  it("probes an unmatched path slash-free before serving the build's 404", async () => {
    const probes: string[] = [];
    const res = await serve(
      get("/unknown/"),
      deps({ trailingSlash: true, pages: ["/a"], probes }),
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("not found");
    expect(probes).toContain("/unknown.html");
    expect(probes.some((key) => key.includes("/unknown/"))).toBe(false);
  });
});

// Next resolves its header routes ahead of the internal trailing-slash
// redirects, in the same list, and applies them to the redirect it answers with
// — so the 308 carries them, on a default config as much as a slashed one.
describe("next.config headers() on the trailing-slash 308", () => {
  const headerRoutes = [
    {
      sourceRegex: "^/(.*)$",
      headers: { "x-frame-options": "DENY", "set-cookie": "banner=1" },
    },
  ];

  it("carries them on the strip redirect", async () => {
    const res = await serve(
      get("/a/?q=1"),
      deps({ pages: ["/a"], files: { "/a.html": "a" }, headerRoutes }),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a?q=1");
    expect(res.headers.get("x-frame-options")).toBe("DENY");
    expect(res.headers.getSetCookie()).toEqual(["banner=1"]);
  });

  it("carries them on the add-slash redirect", async () => {
    const res = await serve(
      get("/a"),
      deps({
        trailingSlash: true,
        pages: ["/a"],
        files: { "/a.html": "a" },
        headerRoutes,
      }),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/a/");
    expect(res.headers.get("x-frame-options")).toBe("DENY");
    expect(res.headers.getSetCookie()).toEqual(["banner=1"]);
  });
});
