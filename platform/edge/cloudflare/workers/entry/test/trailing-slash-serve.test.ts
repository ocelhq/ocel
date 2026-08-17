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

function internalRedirects(trailingSlash: boolean, basePath = ""): unknown[] {
  if (trailingSlash) {
    return [
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
    ...headerRoutes,
    ...internalRedirects(trailingSlash, basePath),
    ...survivingRoutes(basePath),
  ] as Route[];
}

interface Scenario {
  trailingSlash?: boolean;
  skipTrailingSlashRedirect?: boolean;
  basePath?: string;
  pages?: string[];
  files?: Record<string, string>;
  edge?: RouteDeps["edge"];
  middleware?: { entryKey: string; matchers?: { sourceRegex: string }[] };
  dispatch?: RouteDeps["manifest"]["dispatch"];
  functionUrls?: RouteDeps["functionUrls"];
  fetch?: RouteDeps["fetch"];
  cache?: RouteDeps["cache"];
  probes?: string[];
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
    deploymentId: "d1",
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

  it("does not redirect a data request that already carries a trailing slash", async () => {
    const res = await serve(get("/_next/data/t/a.json/"), deps(scenario));
    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/_next/data/t/a.json");
  });
});

describe("trailingSlash: true, basePath: /docs", () => {
  const scenario: Scenario = {
    trailingSlash: true,
    basePath: "/docs",
    pages: ["/docs", "/docs/hello"],
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

describe("routes withoutInternalRedirects keeps", () => {
  for (const trailingSlash of [true, false]) {
    describe(`trailingSlash: ${trailingSlash}`, () => {
      const scenario: Scenario = {
        trailingSlash,
        pages: ["/a", SERVICE_WORKER_PATH],
        files: { "/a.html": "<h1>a</h1>", [SERVICE_WORKER_PATH]: "self.skipWaiting()" },
      };

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

    it("forwards /ssr/ with its slash intact under trailingSlash: true", async () => {
      const { scenario, forwarded } = lambdaScenario({
        route: "/ssr",
        trailingSlash: true,
      });

      const res = await serve(get("/ssr/?q=1"), deps(scenario));

      expect(res.status).toBe(200);
      expect(await res.text()).toBe("ssr");
      const url = new URL(forwarded()!.url);
      expect(url.host).toBe("fn.example.com");
      expect(url.pathname).toBe("/ssr/");
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

    it("forwards /docs/ssr/ with its slash intact under a basePath", async () => {
      const { scenario, forwarded } = lambdaScenario({
        route: "/docs/ssr",
        trailingSlash: true,
        basePath: "/docs",
      });

      const res = await serve(get("/docs/ssr/"), deps(scenario));

      expect(res.status).toBe(200);
      expect(new URL(forwarded()!.url).pathname).toBe("/docs/ssr/");
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

      const redirect = await serve(get("/p"), d);
      expect(redirect.status).toBe(308);
      expect(redirect.headers.get("location")).toBe("/p/");

      const follow = await serve(get("/p/"), d);
      expect(follow.headers.get("x-ocel-cache")).toBe("HIT");
      expect(await follow.text()).toBe("prerendered");
      expect(renders()).toBe(1);
      expect(keys()).toEqual(["https://cache.ocel/p1/web/d1/p"]);
    });

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
      expect(keys()).toEqual(["https://cache.ocel/p1/web/d1/p"]);
    });

    it("gives two apps of one project a key each", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true, true);
      const web = deps(scenario);
      const admin = { ...web, app: "admin" };

      expect((await serve(get("/p"), web)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();
      expect((await serve(get("/p"), admin)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      expect(web.deploymentId).toBe(admin.deploymentId);
      expect(web.manifest.buildId).toBe(admin.manifest.buildId);
      expect(renders()).toBe(2);
      expect(keys()).toEqual([
        "https://cache.ocel/p1/web/d1/p",
        "https://cache.ocel/p1/admin/d1/p",
      ]);
    });

    it("gives two deployments of one app a key each", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true, true);
      const first = deps(scenario);
      const second = { ...first, deploymentId: "d2" };

      expect((await serve(get("/p"), first)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();
      expect((await serve(get("/p"), second)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      expect(first.app).toBe(second.app);
      expect(first.manifest.buildId).toBe(second.manifest.buildId);
      expect(renders()).toBe(2);
      expect(keys()).toEqual([
        "https://cache.ocel/p1/web/d1/p",
        "https://cache.ocel/p1/web/d2/p",
      ]);
    });
  });

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
