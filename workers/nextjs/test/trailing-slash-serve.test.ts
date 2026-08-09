import type { Route } from "@next/routing";
import { describe, expect, it } from "vitest";

import { serve, type RouteDeps } from "../src/index";
import type { AssetBucket } from "../src/assets";

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

// The internal redirects Next unshifts ahead of every other route, verbatim from
// load-custom-routes.ts, as the adapter emits them into beforeMiddleware:
// destination-less rules carrying a Location, a 308, and the `priority` flag the
// build maps Next's `internal: true` onto. Included in every scenario below so
// the tests also prove serve drops them rather than 308ing the canonical form it
// just normalized straight back at the client.
function internalRedirects(trailingSlash: boolean, basePath = ""): Route[] {
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
        sourceRegex: "^/((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/]+\\.\\w+)/$",
        headers: { Location: "/$1" },
        missing: [{ type: "header", key: "x-nextjs-data" }],
        status: 308,
        priority: true,
      },
      {
        sourceRegex: "^/((?!\\.well-known(?:/.*)?)(?:[^/]+/)*[^/\\.]+)$",
        headers: { Location: "/$1/" },
        status: 308,
        priority: true,
      },
    ] as unknown as Route[];
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
    { sourceRegex: "^/(.+?)/$", headers: { Location: "/$1" }, status: 308, priority: true },
  ] as unknown as Route[];
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
        beforeMiddleware: internalRedirects(!!scenario.trailingSlash, basePath),
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch: Object.fromEntries(
        pages.map((path) => [path, { kind: "static" as const }]),
      ),
      middleware: scenario.middleware,
    },
    functionUrls: {},
    slug: "p1",
    app: "web",
    assetStore: assetStoreServing({
      "/404.html": "not found",
      ...(scenario.files ?? {}),
    }),
    edge: scenario.edge,
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
      // The build names the root document /.html, after the route it answers.
      deps({ ...scenario, pages: ["/"], files: { "/.html": "home" } }),
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
    files: { "/docs.html": "docs root", "/docs/hello.html": "hello" },
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
      deps({ ...scenario, pages: ["/"], files: { "/.html": "home" } }),
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
