import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";
import { buildEdgeUrl, readEdgeEntry } from "../src/serve-key";
import { type FakeServing, fakeServing } from "./serving-deps";

const ENTRY_HEADER = "x-ocel-entry";

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
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
    appBuildId: "d1",
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
              kind: "function",
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
          dispatch: { "/api/documents": { kind: "function", id: "/api/documents" } },
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
            "/blog/hello": { kind: "function", id: "bundle-0", entryKey: "" },
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
              kind: "function",
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
          "/": { kind: "function", id: "bundle-0", entryKey: "app/page" },
          "/about": { kind: "function", id: "bundle-0", entryKey: "app/about/page" },
          "/api/hook": {
            kind: "function",
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
    expect(origin.entries()).toEqual(["app/page", "app/about/page", "app/api/hook/route"]);
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

  function blogDeps(origin: ReturnType<typeof recorder>, serving: FakeServing) {
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
      serving: serving.serving,
    });
  }

  it("sends the entry on the bypass path, which forwards the raw headers", async () => {
    const origin = recorder();
    const res = await dispatchTo(
      "/blog",
      blogDeps(origin, fakeServing()),
      new Request("https://app.example/blog", {
        headers: { cookie: "preview=1" },
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.requests[0].headers.get("cookie")).toBe("preview=1");
    expect(origin.entries()).toEqual(["app/blog/page"]);
  });

  it("sends the entry on the request Serve fetches the origin with, past allowHeader's filter", async () => {
    const origin = recorder();
    const serving = fakeServing();

    await dispatchTo("/blog", blogDeps(origin, serving));

    expect(serving.calls).toHaveLength(1);
    expect(serving.calls[0].request.headers.get(ENTRY_HEADER)).toBe("app/blog/page");
    expect(origin.requests).toHaveLength(0);
  });

  it("sends the entry on the on-demand revalidation render", async () => {
    const origin = recorder();
    const serving = fakeServing();

    await dispatchTo(
      "/blog",
      blogDeps(origin, serving),
      new Request("https://app.example/blog", { headers: { "x-prerender-revalidate": "TOKEN" } }),
    );

    expect(origin.entries()).toEqual(["app/blog/page"]);
    expect(origin.requests[0].headers.get("x-prerender-revalidate")).toBe("TOKEN");
  });
});

describe("a PPR prerender whose parent is a node bundle", () => {
  const target = {
    kind: "prerender" as const,
    id: "bundle-0",
    entryKey: "app/ppr/page",
    config: { renderingMode: "PARTIALLY_STATIC" as const },
    fallback: { initialRevalidate: 60 },
  };

  it("reads its shell through Serve and resumes its document at the origin, each with the entry", async () => {
    const origin = recorder();
    const serving = fakeServing(
      () =>
        new Response("STATE<html>shell", {
          headers: { "cf-cache-status": "HIT", "x-ocel-postponed-length": "5" },
        }),
    );

    await dispatchTo(
      "/ppr",
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/ppr": target },
        },
        functionUrls: { "bundle-0": "https://fn.example.com" },
        fetch: origin.fetch,
        serving: serving.serving,
      }),
    );

    expect(serving.calls.map(({ request }) => request.headers.get(ENTRY_HEADER))).toEqual([
      "app/ppr/page",
    ]);
    expect(origin.requests.map(({ method }) => method)).toEqual(["POST"]);
    expect(origin.entries()).toEqual(["app/ppr/page"]);
  });
});

describe("a client-supplied control header", () => {
  const lambdaDeps = (origin: ReturnType<typeof recorder>, target: Record<string, unknown>) =>
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
      lambdaDeps(origin, { kind: "function", id: "legacy" }),
      smuggled({ [ENTRY_HEADER]: "attacker/admin/page" }),
    );

    expect(origin.requests[0].headers.has(ENTRY_HEADER)).toBe(false);
  });

  it("is replaced, not appended to, by the entry the worker chose", async () => {
    const origin = recorder();
    await dispatchTo(
      "/blog/hello",
      lambdaDeps(origin, {
        kind: "function",
        id: "bundle-0",
        entryKey: "app/blog/[slug]/page",
      }),
      smuggled({ [ENTRY_HEADER]: "attacker/admin/page" }),
    );

    expect(origin.requests[0].headers.get(ENTRY_HEADER)).toBe("app/blog/[slug]/page");
    expect([...origin.requests[0].headers].filter(([n]) => n === ENTRY_HEADER)).toHaveLength(1);
  });

  it("is the only x-ocel-* header dropped; the rest of the namespace is the app's", async () => {
    const origin = recorder();
    await dispatchTo(
      "/blog/hello",
      lambdaDeps(origin, { kind: "function", id: "legacy" }),
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
    over: {
      target?: typeof edgeTarget | typeof unnamedTarget;
      functionUrls?: Record<string, string>;
      throughServe?: boolean;
    } = {},
  ): {
    serving: FakeServing;
    deps: RouteDeps;
    invocations: () => string[];
    urls: () => string[];
  } {
    const invocations: string[] = [];
    const urls: string[] = [];
    const serving = fakeServing();
    return {
      serving,
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
        serving: over.throughServe === false ? undefined : serving.serving,
      }),
    };
  }

  it("renders through the edge entry when no Serve is bound", async () => {
    const { deps, invocations } = edgeDeps({ throughServe: false });

    const res = await dispatchTo("/edge-blog", deps);

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
    expect(invocations()).toEqual(["middleware_app/edge-blog"]);
  });

  it("renders through the edge entry with no id to name a parent at all", async () => {
    const { deps, invocations } = edgeDeps({ target: unnamedTarget, throughServe: false });

    const res = await dispatchTo("/edge-blog", deps);

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
    expect(invocations()).toEqual(["middleware_app/edge-blog"]);
  });

  it("502s when neither a Function URL nor an edge entry resolves", async () => {
    const { edgeEntryKey: _none, ...orphan } = unnamedTarget;
    const { deps, invocations } = edgeDeps({ target: orphan as typeof unnamedTarget });

    expect((await dispatchTo("/edge-blog", deps)).status).toBe(502);
    expect(invocations()).toEqual([]);
  });

  it("ignores a Function URL that its id happens to name", async () => {
    const { deps, invocations, urls } = edgeDeps({
      functionUrls: { "/edge-blog": "https://fn.example.com" },
      throughServe: false,
    });

    const res = await dispatchTo("/edge-blog", deps);

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
    expect(invocations()).toEqual(["middleware_app/edge-blog"]);
    expect(urls()).toEqual(["https://app.example/edge-blog"]);
  });

  it("is rendered by Serve from its edge entry at the host it serves, and never by the gateway", async () => {
    const { deps, serving, invocations } = edgeDeps({
      functionUrls: { "/edge-blog": "https://fn.example.com" },
    });

    await dispatchTo("/edge-blog", deps);

    expect(serving.calls).toHaveLength(1);
    expect(readEdgeEntry(serving.calls[0].request.url)).toBe("middleware_app/edge-blog");
    expect(buildEdgeUrl(serving.calls[0].request.url)).toBe("https://shop.example.com/edge-blog");
    expect(invocations()).toEqual([]);
  });
});
