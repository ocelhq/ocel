import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";
import { coloDeps } from "./cache-deps";

const PREFETCH = "purpose";

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

function missingCache(waitUntil: (p: Promise<unknown>) => void = () => {}) {
  return coloDeps({
    cache: {
      match: async () => undefined,
      put: async () => {},
    } as unknown as Cache,
    waitUntil,
  });
}

const isrPrefix = "prod/p/app/build";
const cacheObject = (routePath: string) =>
  `${isrPrefix}/cache/${routePath}.cache.json`;

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

function recorder() {
  const requests: Request[] = [];
  const fetch = (async (request: Request) => {
    requests.push(request);
    return new Response("from-lambda", {
      status: 200,
      headers: { "cache-control": "s-maxage=60" },
    });
  }) as unknown as typeof fetch;
  return {
    requests,
    fetch,
    purposes: () => requests.map((r) => r.headers.get(PREFETCH)),
  };
}

const target = {
  kind: "prerender" as const,
  id: "/blog",
  config: {
    allowHeader: ["host", "purpose"],
    bypassFor: [{ type: "cookie" as const, key: "preview" }],
    bypassToken: "TOKEN",
  },
  fallback: { initialRevalidate: 60 },
};

function blogDeps(
  origin: ReturnType<typeof recorder>,
  over: {
    entry?: unknown;
    now?: number;
    waitUntil?: (p: Promise<unknown>) => void;
    cache?: RouteDeps["cache"];
    interception?: RouteDeps["interception"];
  } = {},
): RouteDeps {
  return {
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: { "/blog": target },
    },
    functionUrls: { "/blog": "https://fn.example.com" },
    slug: "p1",
    app: "web",
    assetStore: noAssets(),
    fetch: origin.fetch,
    cache: "cache" in over ? over.cache : missingCache(over.waitUntil),
    interception:
      "interception" in over
        ? over.interception
        : {
            config: { isrPrefix },
            now: () => over.now ?? 2_000,
            store: storeOf(
              over.entry ? { [cacheObject("blog")]: over.entry } : {},
            ),
          },
  };
}

const storedEntry = (lastModified: number) => ({
  lastModified,
  value: {
    kind: "APP_PAGE",
    html: "<html>from-store</html>",
    status: 200,
    headers: {},
  },
});

const dispatchBlog = (deps: RouteDeps, request?: Request) =>
  dispatchResult(
    { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
    request ?? new Request("https://app.example/blog"),
    deps,
  );

describe("self-revalidation suppression", () => {
  it("stamps purpose: prefetch on the user-path forward to the Lambda", async () => {
    const origin = recorder();

    const res = await dispatchBlog(blogDeps(origin));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.purposes()).toEqual(["prefetch"]);
  });

  it("overwrites whatever purpose the client sent", async () => {
    const origin = recorder();
    const request = new Request("https://app.example/blog", {
      headers: { purpose: "sniff" },
    });

    await dispatchBlog(blogDeps(origin), request);

    expect(origin.purposes()).toEqual(["prefetch"]);
    expect(request.headers.get(PREFETCH)).toBe("sniff");
  });

  it("never stamps the blocking revalidation forward", async () => {
    const pending: Promise<unknown>[] = [];
    const origin = recorder();

    const res = await dispatchBlog(
      blogDeps(origin, {
        entry: storedEntry(1_000),
        now: 1_000 + 61_000,
        waitUntil: (p) => pending.push(p),
      }),
    );
    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    await Promise.all(pending);

    expect(origin.requests).toHaveLength(1);
    expect(origin.requests[0].headers.get("x-prerender-revalidate")).toBe("TOKEN");
    expect(origin.purposes()).toEqual([null]);
  });

  it("never inherits a client-sent purpose on the blocking revalidation forward", async () => {
    const pending: Promise<unknown>[] = [];
    const origin = recorder();

    await dispatchBlog(
      blogDeps(origin, {
        entry: storedEntry(1_000),
        now: 1_000 + 61_000,
        waitUntil: (p) => pending.push(p),
      }),
      new Request("https://app.example/blog", {
        headers: { purpose: "prefetch" },
      }),
    );
    await Promise.all(pending);

    expect(origin.requests).toHaveLength(1);
    expect(origin.purposes()).toEqual([null]);
  });

  it.each([
    [
      "a draft-mode cookie",
      new Request("https://app.example/blog", {
        headers: { cookie: "__prerender_bypass=1" },
      }),
    ],
    [
      "a bypass cookie the route names",
      new Request("https://app.example/blog", {
        headers: { cookie: "preview=1" },
      }),
    ],
    [
      "a non-GET method",
      new Request("https://app.example/blog", { method: "POST" }),
    ],
  ])("never stamps a bypass forward: %s", async (_name, request) => {
    const origin = recorder();

    const res = await dispatchBlog(blogDeps(origin), request);

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.purposes()).toEqual([null]);
  });

  it("never stamps a forward carrying a middleware set-cookie", async () => {
    const origin = recorder();

    const res = await dispatchResult(
      {
        resolvedPathname: "/blog",
        invocationTarget: { pathname: "/blog" },
        middleware: {
          headers: new Headers({ "x-middleware-set-cookie": "session=1" }),
          response: new Response(null),
          result: {},
        },
      },
      new Request("https://app.example/blog"),
      blogDeps(origin),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.purposes()).toEqual([null]);
  });

  it("never stamps when no colo cache is bound, which leaves no tier to refresh", async () => {
    const origin = recorder();

    await dispatchBlog(blogDeps(origin, { cache: undefined }));

    expect(origin.purposes()).toEqual([null]);
  });

  it("never stamps when no ISR store is bound", async () => {
    const origin = recorder();

    await dispatchBlog(blogDeps(origin, { interception: undefined }));

    expect(origin.purposes()).toEqual([null]);
  });

  it("never stamps a pages-router data request", async () => {
    const origin = recorder();

    await dispatchBlog(
      blogDeps(origin),
      new Request("https://app.example/_next/data/t/blog.json"),
    );

    expect(origin.purposes()).toEqual([null]);
  });

  it("never stamps a non-prerender route's forward", async () => {
    const origin = recorder();

    await dispatchResult(
      { resolvedPathname: "/api/hook", invocationTarget: { pathname: "/api/hook" } },
      new Request("https://app.example/api/hook"),
      {
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/api/hook": { kind: "lambda", id: "/api/hook" } },
        },
        functionUrls: { "/api/hook": "https://fn.example.com" },
        slug: "p1",
        app: "web",
        assetStore: noAssets(),
        fetch: origin.fetch,
      },
    );

    expect(origin.purposes()).toEqual([null]);
  });
});
