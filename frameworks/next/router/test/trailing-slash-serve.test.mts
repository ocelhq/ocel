import { describe, expect, it } from "vitest";

import { serve } from "../src/index.mjs";
import {
  SERVICE_WORKER_PATH,
  USER_REDIRECT_FROM,
  deps,
  get,
  type Scenario,
} from "../test-support/serve-scenario.mjs";

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
