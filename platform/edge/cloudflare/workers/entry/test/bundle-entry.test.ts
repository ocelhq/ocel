import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";
import { coloDeps } from "./cache-deps";

const ENTRY_HEADER = "x-ocel-entry";

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
  return { requests, fetch, entries: () => requests.map((r) => r.headers.get(ENTRY_HEADER)) };
}

function deps(over: Partial<RouteDeps>): RouteDeps {
  return {
    manifest: { buildId: "t", basePath: "", pathnames: [], routes: {}, dispatch: {} },
    functionUrls: {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: noAssets(),
    ...over,
  };
}

const dispatchTo = (pathname: string, deps: RouteDeps, request?: Request) =>
  dispatchResult(
    { resolvedPathname: pathname, invocationTarget: { pathname } },
    request ?? new Request(`https://app.example${pathname}`),
    deps,
  );

describe("a lambda route's bundle entry", () => {
  it("forwards x-ocel-entry naming the entry inside the bundle", async () => {
    const origin = recorder();
    const res = await dispatchTo(
      "/blog/hello",
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: {
            "/blog/hello": {
              kind: "lambda",
              id: "bundle-0",
              entryKey: "app/blog/[slug]/page",
            },
          },
        },
        functionUrls: { "bundle-0": "https://fn.example.com" },
        fetch: origin.fetch,
      }),
    );

    expect(res.status).toBe(200);
    expect(origin.requests[0].url).toBe("https://fn.example.com/blog/hello");
    expect(origin.entries()).toEqual(["app/blog/[slug]/page"]);
  });

  it("omits the header entirely for a manifest with no entryKey", async () => {
    const origin = recorder();
    await dispatchTo(
      "/api/documents",
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
        },
        functionUrls: { "/api/documents": "https://fn.example.com" },
        fetch: origin.fetch,
      }),
    );

    expect(origin.requests[0].headers.has(ENTRY_HEADER)).toBe(false);
  });

  it("stamps an empty entry key as an empty header, not as no header", async () => {
    const origin = recorder();
    await dispatchTo(
      "/blog/hello",
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: {
            "/blog/hello": { kind: "lambda", id: "bundle-0", entryKey: "" },
          },
        },
        functionUrls: { "bundle-0": "https://fn.example.com" },
        fetch: origin.fetch,
      }),
      new Request("https://app.example/blog/hello", {
        headers: { [ENTRY_HEADER]: "attacker/admin/page" },
      }),
    );

    expect(origin.requests[0].headers.has(ENTRY_HEADER)).toBe(true);
    expect(origin.entries()).toEqual([""]);
  });

  it("keeps a POST body intact while stamping the entry", async () => {
    const origin = recorder();
    await dispatchTo(
      "/api/documents",
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: {
            "/api/documents": {
              kind: "lambda",
              id: "bundle-0",
              entryKey: "app/api/documents/route",
            },
          },
        },
        functionUrls: { "bundle-0": "https://fn.example.com" },
        fetch: origin.fetch,
      }),
      new Request("https://app.example/api/documents", {
        method: "POST",
        body: "name=cachelab",
      }),
    );

    expect(origin.entries()).toEqual(["app/api/documents/route"]);
    expect(await origin.requests[0].text()).toBe("name=cachelab");
  });

  it("resolves every pathname of one bundle to its single Function URL", async () => {
    const origin = recorder();
    const shared = deps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/": { kind: "lambda", id: "bundle-0", entryKey: "app/page" },
          "/about": { kind: "lambda", id: "bundle-0", entryKey: "app/about/page" },
          "/api/hook": {
            kind: "lambda",
            id: "bundle-0",
            entryKey: "app/api/hook/route",
          },
        },
      },
      functionUrls: { "bundle-0": "https://fn.example.com" },
      fetch: origin.fetch,
    });

    for (const pathname of ["/", "/about", "/api/hook"]) {
      await dispatchTo(pathname, shared);
    }

    expect(origin.requests.map((r) => new URL(r.url).origin)).toEqual([
      "https://fn.example.com",
      "https://fn.example.com",
      "https://fn.example.com",
    ]);
    expect(origin.entries()).toEqual([
      "app/page",
      "app/about/page",
      "app/api/hook/route",
    ]);
  });
});

describe("a prerender whose parent is a node bundle", () => {
  const target = {
    kind: "prerender" as const,
    id: "bundle-0",
    entryKey: "app/blog/page",
    config: {
      allowHeader: ["host", "x-matched-path", "x-prerender-revalidate"],
      bypassFor: [{ type: "cookie" as const, key: "preview" }],
      bypassToken: "TOKEN",
    },
    fallback: { initialRevalidate: 60 },
  };

  function blogDeps(
    origin: ReturnType<typeof recorder>,
    entry: unknown | null,
    over: { now?: number; waitUntil?: (p: Promise<unknown>) => void } = {},
  ): RouteDeps {
    return deps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/blog": target },
      },
      functionUrls: { "bundle-0": "https://fn.example.com" },
      fetch: origin.fetch,
      cache: missingCache(over.waitUntil),
      interception: {
        config: { isrPrefix },
        now: () => over.now ?? 2_000,
        store: storeOf(entry ? { [cacheObject("blog")]: entry } : {}),
      },
    });
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

  it("carries the entry on the bypass path, which forwards the raw headers", async () => {
    const origin = recorder();
    const res = await dispatchTo(
      "/blog",
      blogDeps(origin, null),
      new Request("https://app.example/blog", {
        headers: { cookie: "preview=1" },
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.requests[0].headers.get("cookie")).toBe("preview=1");
    expect(origin.entries()).toEqual(["app/blog/page"]);
  });

  it("carries the entry on the cached-miss path, past allowHeader's filter", async () => {
    const origin = recorder();
    const res = await dispatchTo("/blog", blogDeps(origin, null));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.entries()).toEqual(["app/blog/page"]);
  });

  it("revalidates a stale entry, carrying the entry on the blocking forward", async () => {
    const pending: Promise<unknown>[] = [];
    const origin = recorder();
    const res = await dispatchTo(
      "/blog",
      blogDeps(origin, storedEntry(1_000), {
        now: 1_000 + 61_000,
        waitUntil: (p) => pending.push(p),
      }),
    );

    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>from-store</html>");

    await Promise.all(pending);

    expect(origin.requests).toHaveLength(1);
    expect(origin.requests[0].headers.has("x-prerender-revalidate")).toBe(false);
    expect(origin.entries()).toEqual(["app/blog/page"]);
  });
});

describe("a PPR prerender whose parent is a node bundle", () => {
  const pprTarget = {
    kind: "prerender" as const,
    id: "bundle-0",
    entryKey: "app/ppr/page",
    config: {
      allowHeader: ["host"],
      renderingMode: "PARTIALLY_STATIC" as const,
      bypassToken: "TOKEN",
    },
    fallback: { initialRevalidate: 60 },
  };

  const shellEntry = (lastModified: number) => ({
    lastModified,
    value: {
      kind: "APP_PAGE",
      html: "<html>shell</html>",
      status: 200,
      headers: {},
      postponed: "STATE",
    },
  });

  function pprDeps(
    origin: ReturnType<typeof recorder>,
    lastModified: number,
    over: { now?: number; waitUntil?: (p: Promise<unknown>) => void } = {},
    target: typeof pprTarget | Omit<typeof pprTarget, "entryKey"> = pprTarget,
  ): RouteDeps {
    return deps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/ppr": target },
      },
      functionUrls: { "bundle-0": "https://fn.example.com" },
      fetch: origin.fetch,
      cache: missingCache(over.waitUntil),
      interception: {
        config: { isrPrefix },
        now: () => over.now ?? 2_000,
        store: storeOf({ [cacheObject("ppr")]: shellEntry(lastModified) }),
      },
    });
  }

  it("carries the entry on the resume forward that renders the dynamic half", async () => {
    const origin = recorder();
    const res = await dispatchTo("/ppr", pprDeps(origin, 1_000));

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>shell</html>from-lambda");
    expect(origin.entries()).toEqual(["app/ppr/page"]);
  });

  it("carries the entry on the stale shell's blocking revalidate", async () => {
    const pending: Promise<unknown>[] = [];
    const origin = recorder();
    const res = await dispatchTo(
      "/ppr",
      pprDeps(origin, 1_000, {
        now: 1_000 + 61_000,
        waitUntil: (p) => pending.push(p),
      }),
    );

    expect(await res.text()).toBe("<html>shell</html>from-lambda");
    await Promise.all(pending);

    expect(origin.requests.length).toBeGreaterThanOrEqual(2);
    expect(new Set(origin.entries())).toEqual(new Set(["app/ppr/page"]));
    expect(
      origin.requests.some(
        (r) => r.method === "GET" && r.headers.get("purpose") !== "prefetch",
      ),
    ).toBe(true);
    expect(
      origin.requests.some((r) => r.headers.has("x-prerender-revalidate")),
    ).toBe(false);
  });

  it("drops a client's entry header on the resume of an entryless prerender", async () => {
    const origin = recorder();
    const { entryKey: _omitted, ...entryless } = pprTarget;
    await dispatchTo(
      "/ppr",
      pprDeps(origin, 1_000, {}, entryless),
      new Request("https://app.example/ppr", {
        headers: { [ENTRY_HEADER]: "attacker/admin/page" },
      }),
    );

    expect(origin.requests[0].headers.has(ENTRY_HEADER)).toBe(false);
  });
});

describe("a client-supplied control header", () => {
  const lambdaDeps = (
    origin: ReturnType<typeof recorder>,
    target: Record<string, unknown>,
  ) =>
    deps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/blog/hello": target as never },
      },
      functionUrls: { legacy: "https://fn.example.com", "bundle-0": "https://fn.example.com" },
      fetch: origin.fetch,
    });

  const smuggled = (headers: Record<string, string>) =>
    new Request("https://app.example/blog/hello", { headers });

  it("is never forwarded when the target names no entry", async () => {
    const origin = recorder();
    await dispatchTo(
      "/blog/hello",
      lambdaDeps(origin, { kind: "lambda", id: "legacy" }),
      smuggled({ [ENTRY_HEADER]: "attacker/admin/page" }),
    );

    expect(origin.requests[0].headers.has(ENTRY_HEADER)).toBe(false);
  });

  it("is replaced, not appended to, by the entry the worker chose", async () => {
    const origin = recorder();
    await dispatchTo(
      "/blog/hello",
      lambdaDeps(origin, {
        kind: "lambda",
        id: "bundle-0",
        entryKey: "app/blog/[slug]/page",
      }),
      smuggled({ [ENTRY_HEADER]: "attacker/admin/page" }),
    );

    expect(origin.requests[0].headers.get(ENTRY_HEADER)).toBe(
      "app/blog/[slug]/page",
    );
    expect(
      [...origin.requests[0].headers].filter(([n]) => n === ENTRY_HEADER),
    ).toHaveLength(1);
  });

  it("is the only x-ocel-* header dropped; the rest of the namespace is the app's", async () => {
    const origin = recorder();
    await dispatchTo(
      "/blog/hello",
      lambdaDeps(origin, { kind: "lambda", id: "legacy" }),
      smuggled({
        [ENTRY_HEADER]: "attacker/admin/page",
        "next-resume": "1",
        "x-ocel-probe": "probe-value",
        "x-ocel-injected": "from-the-proxy",
      }),
    );

    const forwarded = origin.requests[0].headers;
    expect(forwarded.has(ENTRY_HEADER)).toBe(false);
    expect(forwarded.has("next-resume")).toBe(false);
    expect(forwarded.get("x-ocel-probe")).toBe("probe-value");
    expect(forwarded.get("x-ocel-injected")).toBe("from-the-proxy");
  });
});

describe("a prerender whose parent renders on the edge", () => {
  const edgeTarget = {
    kind: "prerender" as const,
    id: "/edge-blog",
    edgeEntryKey: "middleware_app/edge-blog",
    config: {},
    fallback: { initialRevalidate: 60 },
  };

  const unnamedTarget = (({ id: _unused, ...rest }) => rest)(edgeTarget);

  function edgeDeps(
    entry: unknown | null,
    now: number,
    pending: Promise<unknown>[],
    over: {
      target?: typeof edgeTarget | typeof unnamedTarget;
      functionUrls?: Record<string, string>;
    } = {},
  ): {
    deps: RouteDeps;
    invocations: () => string[];
    urls: () => string[];
  } {
    const invocations: string[] = [];
    const urls: string[] = [];
    return {
      invocations: () => invocations,
      urls: () => urls,
      deps: deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/edge-blog": over.target ?? edgeTarget },
        },
        functionUrls: over.functionUrls ?? {},
        edge: async (entryKey, request) => {
          invocations.push(entryKey);
          urls.push(request.url);
          return new Response("<html>rendered-on-edge</html>", {
            status: 200,
            headers: { "cache-control": "s-maxage=60" },
          });
        },
        fetch: (async () => {
          throw new Error("an edge-parented prerender must not reach a Function URL");
        }) as unknown as typeof fetch,
        cache: missingCache((p) => pending.push(p)),
        interception: {
          config: { isrPrefix },
          now: () => now,
          store: storeOf(entry ? { [cacheObject("edge-blog")]: entry } : {}),
        },
      }),
    };
  }

  it("renders through the edge entry when no cache tier can answer", async () => {
    const pending: Promise<unknown>[] = [];
    const { deps, invocations } = edgeDeps(null, 2_000, pending);

    const res = await dispatchTo("/edge-blog", deps);

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
    expect(invocations()).toEqual(["middleware_app/edge-blog"]);
  });

  it("never revalidates: an edge render cannot rewrite the entry it refreshes", async () => {
    const pending: Promise<unknown>[] = [];
    const { deps, invocations } = edgeDeps(
      {
        lastModified: 1_000,
        value: {
          kind: "APP_PAGE",
          html: "<html>from-store</html>",
          status: 200,
          headers: {},
        },
      },
      1_000 + 61_000,
      pending,
    );

    const res = await dispatchTo("/edge-blog", deps);

    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>from-store</html>");

    await Promise.all(pending);
    expect(invocations()).toEqual([]);
  });

  it("renders through the edge entry with no id to name a parent at all", async () => {
    const pending: Promise<unknown>[] = [];
    const { deps, invocations } = edgeDeps(null, 2_000, pending, {
      target: unnamedTarget,
    });

    const res = await dispatchTo("/edge-blog", deps);

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
    expect(invocations()).toEqual(["middleware_app/edge-blog"]);
  });

  it("never revalidates an unnamed edge parent either", async () => {
    const pending: Promise<unknown>[] = [];
    const { deps, invocations } = edgeDeps(
      {
        lastModified: 1_000,
        value: {
          kind: "APP_PAGE",
          html: "<html>from-store</html>",
          status: 200,
          headers: {},
        },
      },
      1_000 + 61_000,
      pending,
      { target: unnamedTarget },
    );

    const res = await dispatchTo("/edge-blog", deps);

    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>from-store</html>");

    await Promise.all(pending);
    expect(invocations()).toEqual([]);
  });

  it("502s when neither a Function URL nor an edge entry resolves", async () => {
    const pending: Promise<unknown>[] = [];
    const { edgeEntryKey: _none, ...orphan } = unnamedTarget;
    const { deps, invocations } = edgeDeps(null, 2_000, pending, {
      target: orphan as typeof unnamedTarget,
    });

    expect((await dispatchTo("/edge-blog", deps)).status).toBe(502);
    expect(invocations()).toEqual([]);
  });

  it("ignores a Function URL that its id happens to name", async () => {
    const pending: Promise<unknown>[] = [];
    const { deps, invocations, urls } = edgeDeps(null, 2_000, pending, {
      functionUrls: { "/edge-blog": "https://fn.example.com" },
    });

    const res = await dispatchTo("/edge-blog", deps);

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
    expect(invocations()).toEqual(["middleware_app/edge-blog"]);
    expect(urls()).toEqual(["https://app.example/edge-blog"]);
  });
});
