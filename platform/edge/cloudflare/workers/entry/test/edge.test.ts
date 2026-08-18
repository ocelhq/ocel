import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { dispatchResult, serve, type RouteDeps } from "../src/index";
import { coloDeps } from "./cache-deps";
import {
  createEdgeInvoker,
  type EdgeCacheBinding,
  type EdgeCacheStub,
  type EdgeEntryKind,
  type EdgeInvoker,
  type EdgeObjectStore,
  type EdgeVariables,
} from "../src/edge";
import type { AssetBucket } from "@framework/next-router/assets";
import type { ObjectStoreReader } from "../src/tag-clock";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    LOADER: WorkerLoader;
    DEPLOYMENTS: Fetcher;
  }
}

const remoteStub = () => env.DEPLOYMENTS as unknown as EdgeCacheStub;

interface EdgeEntry {
  chunks: string[];
  handlerExport: string;
}

function shimFor(
  entries: Record<string, EdgeEntry>,
  assetIdByName: Record<string, string> = {},
): string {
  return `import { AsyncLocalStorage } from "node:async_hooks"

const ENTRIES = ${JSON.stringify(entries)}
const ASSETS = ${JSON.stringify(assetIdByName)}

const ocelFetch = globalThis.fetch

const ocelAssetId = (url) => {
  if (typeof url !== "string" || !url.startsWith("blob:")) return undefined
  const name = url.slice(5)
  if (Object.hasOwn(ASSETS, name)) return ASSETS[name]
  let decoded
  try {
    decoded = decodeURIComponent(name)
  } catch {
    return undefined
  }
  return Object.hasOwn(ASSETS, decoded) ? ASSETS[decoded] : undefined
}

globalThis.fetch = (input, init) => {
  const url =
    typeof input === "string" ? input : input instanceof URL ? input.href : input?.url
  const id = ocelAssetId(url)
  if (id === undefined) return ocelFetch(input, init)
  return import("./" + id).then((m) => new Response(m.default))
}

export default {
  async fetch(request, env, ctx) {
    globalThis.AsyncLocalStorage ??= AsyncLocalStorage
    globalThis.process ??= { env: {} }
    Object.assign(globalThis.process.env, env)
    globalThis.__OCEL_EDGE_CACHE = { rpc: env.OCEL_CACHE_RPC, scope: env.OCEL_CACHE_SCOPE }
    const k = ctx.props.entryKey
    globalThis.__OCEL_EDGE_ENTRY = k
    const e = ENTRIES[k]
    if (!e) return new Response(\`unknown edge entry \${k}\`, { status: 500 })
    for (const id of e.chunks) await import("./" + id)
    const entry = await globalThis._ENTRIES[k]
    const handler = entry[e.handlerExport]
    return handler(request, {
      waitUntil: (p) => ctx.waitUntil(p),
      signal: request.signal,
      requestMeta: {},
    })
  },
}
`;
}

function chunkFor(entryKey: string, handler: string): string {
  return `globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = { handler: ${handler} }
`;
}

function nodeRequireChunkFor(entryKey: string): string {
  return `const { Buffer } = require("node:buffer")
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () => new Response(Buffer.from("required").toString()),
}
`;
}

function storedJson(json: string) {
  return {
    text: async () => json,
    arrayBuffer: async () => new TextEncoder().encode(json).buffer,
  };
}

function base64Of(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes));
}

let bundles = 0;

function invokerFor(
  handlers: Record<string, string>,
  chunkSource: (entryKey: string, handler: string) => string = chunkFor,
  cache?: EdgeCacheBinding,
  id?: string,
  assetBytes: Record<string, Uint8Array> = {},
): EdgeInvoker {
  const chunks: Record<string, string> = {};
  const entries: Record<string, EdgeEntry> = {};
  let next = 0;
  for (const [entryKey, handler] of Object.entries(handlers)) {
    const id = `c/${next++}.js`;
    chunks[id] = chunkSource(entryKey, handler);
    entries[entryKey] = { chunks: [id], handlerExport: "handler" };
  }

  const assets: Record<string, string> = {};
  const assetIdByName: Record<string, string> = {};
  let nextAsset = 0;
  for (const [name, bytes] of Object.entries(assetBytes)) {
    const id = `a/${nextAsset++}.bin`;
    assets[id] = base64Of(bytes);
    assetIdByName[name] = id;
  }

  const json = JSON.stringify({
    version: 2,
    mainModule: "main.js",
    shim: shimFor(entries, assetIdByName),
    chunks,
    wasm: {},
    assets,
    env: { __NEXT_BUILD_ID: "t" },
    entries,
  });

  const seq = bundles++;
  const bundleKey = `edge/${seq}/bundle.json`;
  const store: EdgeObjectStore = {
    async get(key) {
      return key === bundleKey ? storedJson(json) : null;
    },
  };
  return createEdgeInvoker(
    env.LOADER,
    {
      bundleKey,
      id: id ?? `edge-${seq}`,
      compatDate: "2026-03-10",
      compatFlags: ["nodejs_compat"],
    },
    store,
    cache,
  );
}

function futureBundleInvoker(): EdgeInvoker {
  const seq = bundles++;
  const bundleKey = `edge/${seq}/bundle.json`;
  const json = JSON.stringify({
    version: 3,
    mainModule: "main.js",
    shim: shimFor({}),
    chunks: {},
    entries: {},
  });
  return createEdgeInvoker(
    env.LOADER,
    { bundleKey, id: `edge-future-${seq}`, compatDate: "2026-03-10" },
    { async get(key) { return key === bundleKey ? storedJson(json) : null; } },
  );
}

function missingBundleInvoker(): EdgeInvoker {
  const seq = bundles++;
  return createEdgeInvoker(
    env.LOADER,
    {
      bundleKey: `edge/${seq}/gone.json`,
      id: `edge-missing-${seq}`,
      compatDate: "2026-03-10",
    },
    { async get() { return null; } },
  );
}

const MIDDLEWARE_KEY = "middleware_middleware";

function middlewareInvoker(handler: string): EdgeInvoker {
  return invokerFor({ [MIDDLEWARE_KEY]: handler });
}

function counted(edge: EdgeInvoker): { edge: EdgeInvoker; calls: () => number } {
  let calls = 0;
  return {
    edge: (entryKey, request) => {
      calls++;
      return edge(entryKey, request);
    },
    calls: () => calls,
  };
}

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

const emptyRoutes = {
  beforeMiddleware: [],
  beforeFiles: [],
  afterFiles: [],
  dynamicRoutes: [],
  onMatch: [],
  fallback: [],
};

function deps(overrides: Partial<RouteDeps> = {}): RouteDeps {
  return {
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: emptyRoutes,
      dispatch: {},
    },
    functionUrls: {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: assetStoreServing({}),
    ...overrides,
  } as RouteDeps;
}

function staticDeps(
  middleware: unknown,
  overrides: Partial<RouteDeps> = {},
): RouteDeps {
  return deps({
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: ["/static.txt"],
      routes: emptyRoutes,
      dispatch: { "/static.txt": { kind: "static" } },
      middleware,
    },
    assetStore: assetStoreServing({ "/static.txt": "the-file" }),
    ...overrides,
  } as Partial<RouteDeps>);
}

const passthrough = () =>
  counted(
    middlewareInvoker(
      `async () => new Response(null, { headers: { "x-middleware-next": "1", "x-mw-ran": "1" } })`,
    ),
  );

async function gated(
  matchers: unknown,
  init?: RequestInit,
): Promise<{ res: Response; calls: number }> {
  const { edge, calls } = passthrough();
  const middleware = { entryKey: MIDDLEWARE_KEY, ...(matchers !== undefined && { matchers }) };
  const res = await serve(
    new Request("https://app.example/static.txt", init),
    staticDeps(middleware, { edge }),
  );
  return { res, calls: calls() };
}

describe("middleware matchers", () => {
  it("does not invoke middleware for a path its matchers exclude", async () => {
    const { res, calls } = await gated([{ sourceRegex: "^/dashboard$" }]);

    expect(calls).toBe(0);
    expect(await res.text()).toBe("the-file");
    expect(res.headers.get("x-mw-ran")).toBe(null);
  });

  it("invokes middleware on every path when the matcher list is absent", async () => {
    const { res, calls } = await gated(undefined);

    expect(calls).toBe(1);
    expect(res.headers.get("x-mw-ran")).toBe("1");
  });

  it("invokes middleware on every path when the matcher list is empty", async () => {
    const { res, calls } = await gated([]);

    expect(calls).toBe(1);
    expect(res.headers.get("x-mw-ran")).toBe("1");
  });

  it("invokes middleware for a path its matchers name", async () => {
    const { res, calls } = await gated([{ sourceRegex: "^/static\\.txt$" }]);

    expect(calls).toBe(1);
    expect(res.headers.get("x-mw-ran")).toBe("1");
  });

  it("honours a matcher's has condition", async () => {
    const matchers = [
      {
        sourceRegex: "^/static\\.txt$",
        has: [{ type: "header", key: "x-gate", value: "on" }],
      },
    ];

    expect((await gated(matchers)).calls).toBe(0);
    expect(
      (await gated(matchers, { headers: { "x-gate": "on" } })).calls,
    ).toBe(1);
  });

  it("honours a matcher's missing condition", async () => {
    const matchers = [
      {
        sourceRegex: "^/static\\.txt$",
        missing: [{ type: "header", key: "x-skip" }],
      },
    ];

    expect((await gated(matchers)).calls).toBe(1);
    expect(
      (await gated(matchers, { headers: { "x-skip": "1" } })).calls,
    ).toBe(0);
  });
});

describe("middleware matchers under i18n", () => {
  const I18N = { locales: ["en", "fr"], defaultLocale: "en" };

  function localeDeps(matchers: unknown, edge?: EdgeInvoker): RouteDeps {
    const pathnames = [
      "/en/foo",
      "/fr/foo",
      "/en/bar",
      "/fr/bar",
      "/_next/data/t/en/foo.json",
      "/_next/data/t/fr/foo.json",
    ];
    return deps({
      manifest: {
        buildId: "t",
        basePath: "",
        i18n: I18N,
        pathnames,
        routes: emptyRoutes,
        dispatch: Object.fromEntries(pathnames.map((path) => [path, { kind: "static" }])),
        middleware: { entryKey: MIDDLEWARE_KEY, matchers },
      },
      assetStore: assetStoreServing({
        "/en/foo.html": "the en page",
        "/fr/foo.html": "the fr page",
        "/en/bar.html": "the en bar page",
        "/fr/bar.html": "the fr bar page",
        "/_next/data/t/en/foo.json": '{"at":"en/foo"}',
        "/_next/data/t/fr/foo.json": '{"at":"fr/foo"}',
        "/en/404.html": "not found",
      }),
      edge: edge ?? passthrough().edge,
    } as Partial<RouteDeps>);
  }

  it("matches an unprefixed default-locale path against a mandatory locale group", async () => {
    const { edge, calls } = passthrough();
    const res = await serve(
      new Request("https://app.example/foo"),
      localeDeps([{ sourceRegex: "^/(?:en|fr)/foo$" }], edge),
    );

    expect(res.headers.get("x-mw-ran")).toBe("1");
    expect(calls()).toBe(1);
    expect(await res.text()).toBe("the en page");
  });

  it("still leaves a genuinely unmatched path alone", async () => {
    const { edge, calls } = passthrough();
    const res = await serve(
      new Request("https://app.example/bar"),
      localeDeps([{ sourceRegex: "^/(?:en|fr)/foo$" }], edge),
    );

    expect(res.headers.get("x-mw-ran")).toBe(null);
    expect(calls()).toBe(0);
    expect(await res.text()).toBe("the en bar page");
  });

  it("resolves a _next/data page URL with no locale of its own", async () => {
    const res = await serve(
      new Request("https://app.example/_next/data/t/foo.json", {
        headers: { "x-nextjs-data": "1" },
      }),
      localeDeps(undefined),
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/_next/data/t/en/foo.json");
    expect(await res.text()).toBe('{"at":"en/foo"}');
  });
});

describe("middleware responses", () => {
  it("returns the middleware's own body and status when it answers the request", async () => {
    const edge = middlewareInvoker(
      `async () => new Response(JSON.stringify({ error: "nope" }), {
         status: 401,
         headers: { "content-type": "application/json" },
       })`,
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps({ entryKey: MIDDLEWARE_KEY }, { edge }),
    );

    expect(res.status).toBe(401);
    expect(res.headers.get("content-type")).toBe("application/json");
    expect(await res.json()).toEqual({ error: "nope" });
  });

  it("strips the x-middleware-* control headers off a middleware response", async () => {
    const edge = middlewareInvoker(
      `async () => new Response("denied", {
         status: 403,
         headers: { "x-middleware-set-cookie": "sid=1", "x-keep": "1" },
       })`,
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps({ entryKey: MIDDLEWARE_KEY }, { edge }),
    );

    expect(res.status).toBe(403);
    expect(res.headers.get("x-middleware-set-cookie")).toBe(null);
    expect(res.headers.get("x-keep")).toBe("1");
  });

  it.each([204, 304])(
    "returns a middleware's %i, which carries no body at all",
    async (status) => {
      const edge = middlewareInvoker(
        `async () => new Response(null, { status: ${status} })`,
      );

      const res = await serve(
        new Request("https://app.example/static.txt"),
        staticDeps({ entryKey: MIDDLEWARE_KEY }, { edge }),
      );

      expect(res.status).toBe(status);
    },
  );

  it("keeps the x-middleware-* control headers off a rewritten response", async () => {
    const edge = middlewareInvoker(
      `async () => new Response(null, {
         headers: {
           "x-middleware-rewrite": "/dest",
           "x-middleware-request-x-tenant": "acme",
           "x-keep": "1",
         },
       })`,
    );

    const res = await serve(
      new Request("https://app.example/from"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/dest"],
          routes: emptyRoutes,
          dispatch: { "/dest": { kind: "lambda", id: "/dest" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        functionUrls: { "/dest": "https://fn.example.com" },
        edge,
        fetch: (async () => new Response("origin")) as unknown as typeof fetch,
      } as Partial<RouteDeps>),
    );

    expect(await res.text()).toBe("origin");
    expect(res.headers.get("x-keep")).toBe("1");
    expect(
      [...res.headers.keys()].filter((n) => n.startsWith("x-middleware-")),
    ).toEqual([]);
  });

  it("redirects with the middleware's Set-Cookie intact", async () => {
    const edge = middlewareInvoker(
      `async () => new Response(null, {
         status: 307,
         headers: { location: "/login", "set-cookie": "sid=abc; Path=/" },
       })`,
    );

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps({ entryKey: MIDDLEWARE_KEY }, { edge }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/login");
    expect(res.headers.getSetCookie()).toEqual(["sid=abc; Path=/"]);
  });

  it("forwards a middleware request-header override to the origin", async () => {
    let captured: Request | undefined;
    const edge = middlewareInvoker(
      `async () => new Response(null, {
         headers: {
           "x-middleware-next": "1",
           "x-middleware-override-headers": "x-user",
           "x-middleware-request-x-user": "alice",
         },
       })`,
    );

    await serve(
      new Request("https://app.example/api/me"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/api/me"],
          routes: emptyRoutes,
          dispatch: { "/api/me": { kind: "lambda", id: "/api/me" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        functionUrls: { "/api/me": "https://fn.example.com" },
        edge,
        fetch: (async (req: Request) => {
          captured = req;
          return new Response("ok");
        }) as unknown as typeof fetch,
      } as Partial<RouteDeps>),
    );

    expect(captured?.headers.get("x-user")).toBe("alice");
  });

  it("leaves the origin a readable body after middleware consumed one", async () => {
    let captured: Request | undefined;
    const edge = middlewareInvoker(
      `async (request) => new Response(null, {
         headers: { "x-middleware-next": "1", "x-mw-body": await request.text() },
       })`,
    );

    const res = await serve(
      new Request("https://app.example/api/me", {
        method: "POST",
        body: "hello=world",
      }),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/api/me"],
          routes: emptyRoutes,
          dispatch: { "/api/me": { kind: "lambda", id: "/api/me" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        functionUrls: { "/api/me": "https://fn.example.com" },
        edge,
        fetch: (async (req: Request) => {
          captured = req;
          return new Response("ok");
        }) as unknown as typeof fetch,
      } as Partial<RouteDeps>),
    );

    expect(res.headers.get("x-mw-body")).toBe("hello=world");
    expect(await captured?.text()).toBe("hello=world");
  });
});

describe("resolvedHeaders", () => {
  it("applies a next.config headers() rule to the served response", async () => {
    const res = await serve(
      new Request("https://app.example/static.txt"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/static.txt"],
          routes: {
            ...emptyRoutes,
            onMatch: [
              {
                sourceRegex: "^/static\\.txt$",
                headers: { "x-frame-options": "DENY" },
              },
            ],
          },
          dispatch: { "/static.txt": { kind: "static" } },
        },
        assetStore: assetStoreServing({ "/static.txt": "the-file" }),
      } as Partial<RouteDeps>),
    );

    expect(res.headers.get("x-frame-options")).toBe("DENY");
    expect(await res.text()).toBe("the-file");
  });
});

describe("edge dispatch", () => {
  it("invokes an edge route's entry through the loader", async () => {
    const edge = invokerFor({
      "middleware_app/edge": `async (request) =>
         new Response("from-edge:" + new URL(request.url).pathname + ":" + process.env.__NEXT_BUILD_ID)`,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-edge:/edge:t");
  });

  it("invokes an entry whose chunk requires a Node builtin", async () => {
    const edge = invokerFor(
      { "middleware_app/edge": "" },
      (entryKey) => nodeRequireChunkFor(entryKey),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("required");
  });

  it("invokes an entry whose chunk reads the AsyncLocalStorage global", async () => {
    const edge = invokerFor(
      { "middleware_app/edge": "" },
      (entryKey) => `if (typeof AsyncLocalStorage !== "function") {
  throw new Error("Invariant: AsyncLocalStorage accessed in runtime where it is not available")
}
const store = new AsyncLocalStorage()
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () => store.run("scoped", () => new Response(store.getStore())),
}
`,
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("scoped");
  });

  it("publishes the entry key on a global before an entry's chunks evaluate", async () => {
    const edge = invokerFor(
      { "middleware_app/edge": "" },
      (entryKey) => `const seenAtModuleScope = globalThis.__OCEL_EDGE_ENTRY
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () => new Response(String(seenAtModuleScope)),
}
`,
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("middleware_app/edge");
  });

  it("answers a blob: fetch from the bundle's assets, byte for byte", async () => {
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe]);
    const edge = invokerFor(
      {
        "middleware_app/edge": `async () =>
           new Response(await (await fetch(new URL("blob:server/edge/assets/pic.png"))).arrayBuffer())`,
      },
      undefined,
      undefined,
      undefined,
      { "server/edge/assets/pic.png": bytes },
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(new Uint8Array(await res.arrayBuffer())).toEqual(bytes);
  });

  it("hands a fetch the asset table does not name to the platform", async () => {
    const edge = invokerFor(
      {
        "middleware_app/edge": `async () => {
           const res = await fetch("data:text/plain,from-the-platform")
           return new Response(res.status + ":" + (await res.text()))
         }`,
      },
      undefined,
      undefined,
      undefined,
      { "server/edge/assets/pic.png": new Uint8Array([1, 2, 3]) },
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(await res.text()).toBe("200:from-the-platform");
  });

  it("hands an unknown blob: name to the platform, which rejects it", async () => {
    const edge = invokerFor(
      {
        "middleware_app/edge": `async () => {
           try {
             await fetch("blob:server/edge/assets/absent.png")
             return new Response("answered-from-table")
           } catch (e) {
             return new Response(e.name + "|" + e.message)
           }
         }`,
      },
      undefined,
      undefined,
      undefined,
      { "server/edge/assets/pic.png": new Uint8Array([1, 2, 3]) },
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(await res.text()).toBe(
      "TypeError|Fetch API cannot load: blob:server/edge/assets/absent.png",
    );
  });

  it("resolves an asset whose name the URL constructor percent-encodes", async () => {
    const bytes = new Uint8Array([0xde, 0xad, 0xbe, 0xef]);
    const edge = invokerFor(
      {
        "middleware_app/edge": `async () =>
           new Response(await (await fetch(new URL("blob:server/edge/assets/caf\u00e9.png"))).arrayBuffer())`,
      },
      undefined,
      undefined,
      undefined,
      { "server/edge/assets/caf\u00e9.png": bytes },
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(new Uint8Array(await res.arrayBuffer())).toEqual(bytes);
  });

  it("serves an entry from a bundle carrying an asset that is not valid UTF-8", async () => {
    const edge = invokerFor(
      {
        "middleware_app/other": `async () => new Response("other-entry")`,
        "middleware_app/edge": `async () => new Response("edge-entry")`,
      },
      undefined,
      undefined,
      undefined,
      {
        "server/edge/assets/pic.png": new Uint8Array([
          0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe,
        ]),
      },
    );

    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("edge-entry");
  });

  it("returns 500 when the bundle cannot be read", async () => {
    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge: missingBundleInvoker(),
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(500);
  });

  it("returns 500 for a bundle whose version this worker cannot read", async () => {
    const res = await dispatchResult(
      { resolvedPathname: "/edge", invocationTarget: { pathname: "/edge" } },
      new Request("https://app.example/edge"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/edge"],
          routes: emptyRoutes,
          dispatch: {
            "/edge": { kind: "edge", entryKey: "middleware_app/edge" },
          },
        },
        edge: futureBundleInvoker(),
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(500);
  });

  it("returns 500 rather than routing on when middleware cannot be invoked", async () => {
    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps({ entryKey: MIDDLEWARE_KEY }, { edge: missingBundleInvoker() }),
    );

    expect(res.status).toBe(500);
    expect(await res.text()).not.toContain("the-file");
  });

  it("returns 500 when middleware itself throws", async () => {
    const edge = middlewareInvoker(`async () => { throw new Error("boom") }`);

    const res = await serve(
      new Request("https://app.example/static.txt"),
      staticDeps({ entryKey: MIDDLEWARE_KEY }, { edge }),
    );

    expect(res.status).toBe(500);
  });
});

describe("edge-parented prerenders", () => {
  const isrPrefix = "prod/p/app/build";

  function storeOf(entries: Record<string, unknown>): ObjectStoreReader {
    return {
      async get(key) {
        const entry = entries[key];
        return entry === undefined ? null : storedJson(JSON.stringify(entry));
      },
    };
  }

  function prerenderDeps(entry: unknown | null, edge: EdgeInvoker): RouteDeps {
    return deps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/edge-blog"],
        routes: emptyRoutes,
        dispatch: {
          "/edge-blog": {
            kind: "prerender",
            id: "/edge-blog",
            edgeEntryKey: "middleware_app/edge-blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: {},
      edge,
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
        store: storeOf(
          entry ? { [`${isrPrefix}/cache/edge-blog.cache.json`]: entry } : {},
        ),
      },
    } as Partial<RouteDeps>);
  }

  const dispatchBlog = (deps: RouteDeps) =>
    dispatchResult(
      {
        resolvedPathname: "/edge-blog",
        invocationTarget: { pathname: "/edge-blog" },
      },
      new Request("https://app.example/edge-blog"),
      deps,
    );

  const renderer = () =>
    invokerFor({
      "middleware_app/edge-blog": `async () =>
         new Response("<html>rendered-on-edge</html>", {
           status: 200,
           headers: { "cache-control": "s-maxage=60" },
         })`,
    });

  it("serves an edge-parented prerender from the R2 entry without invoking the entry", async () => {
    const { edge, calls } = counted(renderer());
    const res = await dispatchBlog(
      prerenderDeps(
        {
          lastModified: 1_000,
          value: {
            kind: "APP_PAGE",
            html: "<html>from-store</html>",
            status: 200,
            headers: {},
          },
        },
        edge,
      ),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>from-store</html>");
    expect(calls()).toBe(0);
  });

  it("invokes the edge entry when the R2 entry misses, with no Function URL bound", async () => {
    const res = await dispatchBlog(prerenderDeps(null, renderer()));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
  });

  it("still 502s a prerender with neither a Function URL nor an edge entry", async () => {
    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog"),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/blog"],
          routes: emptyRoutes,
          dispatch: { "/blog": { kind: "prerender", id: "/blog", config: {} } },
        },
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(502);
  });
});

describe("the cache loopback", () => {
  it("stays live across every request a warm isolate serves", async () => {
    const edge = invokerFor(
      {
        e: `async () => {
          const cache = globalThis.__OCEL_EDGE_CACHE
          const probe = await cache.rpc.fetch("https://loopback/")
          return new Response(cache.scope + ":" + probe.status)
        }`,
      },
      chunkFor,
      { rpc: remoteStub(), scope: "prod/proj/app/build" },
    );

    for (let i = 0; i < 3; i++) {
      const response = await edge("e", new Request("https://x/"));
      expect(await response.text()).toBe("prod/proj/app/build:501");
    }
  });

  it("leaves the bundle's own env intact", async () => {
    const edge = invokerFor(
      { e: `async () => new Response(process.env.__NEXT_BUILD_ID)` },
      chunkFor,
      { rpc: remoteStub(), scope: "prod/proj/app/build" },
    );
    expect(await (await edge("e", new Request("https://x/"))).text()).toBe("t");
  });

  it("is simply absent when nothing bound one", async () => {
    const edge = invokerFor({
      e: `async () => new Response(String(globalThis.__OCEL_EDGE_CACHE.rpc))`,
    });
    expect(await (await edge("e", new Request("https://x/"))).text()).toBe("undefined");
  });
});

describe("the loaded isolate's identity", () => {
  const servedCount = (entryKey: string) => `let served = 0
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () =>
    new Response(++served + ":" + globalThis.__OCEL_EDGE_CACHE.scope),
}
`;

  const deployment = (id: string, scope: string) =>
    invokerFor({ e: "" }, servedCount, { rpc: remoteStub(), scope }, id);

  const serve = async (edge: EdgeInvoker, kind?: EdgeEntryKind) =>
    (await edge("e", new Request("https://x/"), kind)).text();

  it("reuses one isolate across the requests of one deployment", async () => {
    const prod = deployment("shared-bundle-a", "prod/p/app/b1");
    const same = deployment("shared-bundle-a", "prod/p/app/b1");

    expect(await serve(prod)).toBe("1:prod/p/app/b1");
    expect(await serve(same)).toBe("2:prod/p/app/b1");
  });

  it("separates two deployments that share a bundle but not a scope", async () => {
    const prod = deployment("shared-bundle-b", "prod/p/app/b1");
    const preview = deployment("shared-bundle-b", "preview/p/app/b1");

    expect(await serve(prod)).toBe("1:prod/p/app/b1");
    expect(await serve(preview)).toBe("1:preview/p/app/b1");
  });

  it("keeps a middleware entry off the isolate a page entry already warmed", async () => {
    const bundle = deployment("shared-bundle-c", "prod/p/app/b1");

    expect(await serve(bundle)).toBe("1:prod/p/app/b1");
    expect(await serve(bundle, "middleware")).toBe("1:prod/p/app/b1");
  });

  it("reuses the middleware isolate across its own requests", async () => {
    const bundle = deployment("shared-bundle-d", "prod/p/app/b1");

    expect(await serve(bundle, "middleware")).toBe("1:prod/p/app/b1");
    expect(await serve(bundle, "middleware")).toBe("2:prod/p/app/b1");
    expect(await serve(bundle)).toBe("1:prod/p/app/b1");
  });
});

describe("the URL middleware is handed", () => {
  const PAGE = "/send-url";

  const echo = () =>
    middlewareInvoker(
      `async (request) => new Response(null, {
         headers: {
           "x-middleware-next": "1",
           "req-url-path": new URL(request.url).pathname,
         },
       })`,
    );

  interface Scenario {
    trailingSlash?: boolean;
    skipTrailingSlashRedirect?: boolean;
    skipMiddlewareUrlNormalize?: boolean;
    matchers?: unknown;
    edge?: EdgeInvoker;
  }

  function pageDeps(scenario: Scenario): RouteDeps {
    const dataPaths = [`/_next/data/t${PAGE}.json`, "/_next/data/t/index.json"];
    const pathnames = [PAGE, "/", ...dataPaths];
    return deps({
      manifest: {
        buildId: "t",
        basePath: "",
        trailingSlash: scenario.trailingSlash,
        skipTrailingSlashRedirect: scenario.skipTrailingSlashRedirect,
        skipMiddlewareUrlNormalize: scenario.skipMiddlewareUrlNormalize,
        pathnames,
        routes: emptyRoutes,
        dispatch: Object.fromEntries(
          pathnames.map((path) => [path, { kind: "static" }]),
        ),
        middleware: {
          entryKey: MIDDLEWARE_KEY,
          ...(scenario.matchers !== undefined && { matchers: scenario.matchers }),
        },
      },
      assetStore: assetStoreServing({
        [`${PAGE}.html`]: "the page",
        "/.html": "the root",
        "/404.html": "not found",
        ...Object.fromEntries(dataPaths.map((path) => [path, `{"at":"${path}"}`])),
      }),
      edge: scenario.edge ?? echo(),
    } as Partial<RouteDeps>);
  }

  function get(path: string, headers: Record<string, string> = {}) {
    return new Request(`https://app.example${path}`, { redirect: "manual", headers });
  }

  it("hands middleware the slashed pathname under trailingSlash: true", async () => {
    const res = await serve(get(`${PAGE}/`), pageDeps({ trailingSlash: true }));

    expect(res.status).toBe(200);
    expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    expect(await res.text()).toBe("the page");
  });

  it("hands middleware the bare pathname under trailingSlash: false", async () => {
    const res = await serve(get(PAGE), pageDeps({}));

    expect(res.status).toBe(200);
    expect(res.headers.get("req-url-path")).toBe(PAGE);
    expect(await res.text()).toBe("the page");
  });

  describe("a data request", () => {
    it.each([
      [true, `${PAGE}/`],
      [false, PAGE],
    ])(
      "is shown to middleware as its page under trailingSlash: %s",
      async (trailingSlash, expected) => {
        const res = await serve(
          get(`/_next/data/t${PAGE}.json`, { "x-nextjs-data": "1" }),
          pageDeps({ trailingSlash }),
        );

        expect(res.headers.get("req-url-path")).toBe(expected);
        expect(res.headers.get("x-matched-path")).toBe(`/_next/data/t${PAGE}.json`);
        expect(await res.text()).toBe(`{"at":"/_next/data/t${PAGE}.json"}`);
      },
    );

    it.each([true, false])(
      "for index is shown as the root under trailingSlash: %s",
      async (trailingSlash) => {
        const res = await serve(
          get("/_next/data/t/index.json", { "x-nextjs-data": "1" }),
          pageDeps({ trailingSlash }),
        );

        expect(res.headers.get("req-url-path")).toBe("/");
        expect(res.headers.get("x-matched-path")).toBe("/_next/data/t/index.json");
      },
    );
  });

  describe("matchers, which are tested against that same URL", () => {
    it("runs a matcher written against the canonical slashed form", async () => {
      const { edge, calls } = counted(echo());
      const res = await serve(
        get(`${PAGE}/`),
        pageDeps({ trailingSlash: true, matchers: [{ sourceRegex: "^/send-url/$" }], edge }),
      );

      expect(calls()).toBe(1);
      expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    });

    it("loses no match for one path-to-regexp wrote with the optional slash", async () => {
      const { edge, calls } = counted(echo());
      await serve(
        get(`${PAGE}/`),
        pageDeps({
          trailingSlash: true,
          matchers: [{ sourceRegex: "^/send-url(?:\\/(?=$))?$" }],
          edge,
        }),
      );

      expect(calls()).toBe(1);
    });

    it("still does not run one whose matcher names another path", async () => {
      const { edge, calls } = counted(echo());
      const res = await serve(
        get(`${PAGE}/`),
        pageDeps({ trailingSlash: true, matchers: [{ sourceRegex: "^/other$" }], edge }),
      );

      expect(calls()).toBe(0);
      expect(await res.text()).toBe("the page");
    });
  });

  it("routes a canonical rewrite destination on its routing form, forwarding it slashed", async () => {
    const edge = middlewareInvoker(
      `async () => new Response(null, { headers: { "x-middleware-rewrite": "/b/" } })`,
    );

    const res = await serve(
      get(`${PAGE}/`),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          trailingSlash: true,
          pathnames: [PAGE, "/b"],
          routes: emptyRoutes,
          dispatch: { [PAGE]: { kind: "static" }, "/b": { kind: "lambda", id: "/b" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        assetStore: assetStoreServing({ [`${PAGE}.html`]: "the page", "/404.html": "not found" }),
        functionUrls: { "/b": "https://fn.example.com" },
        edge,
        fetch: (async (input: Request) =>
          new Response(new URL(input.url).pathname)) as unknown as typeof fetch,
      } as Partial<RouteDeps>),
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/b");
    expect(await res.text()).toBe("/b/");
  });

  it("forwards a rewrite to another origin byte-verbatim", async () => {
    const edge = middlewareInvoker(
      `async () => new Response(null, {
         headers: { "x-middleware-rewrite": "https://ext.example.com/b/?q=1" },
       })`,
    );
    let forwarded: string | undefined;

    const res = await serve(
      get(`${PAGE}/`),
      deps({
        manifest: {
          buildId: "t",
          basePath: "",
          trailingSlash: true,
          pathnames: [PAGE],
          routes: emptyRoutes,
          dispatch: { [PAGE]: { kind: "static" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        assetStore: assetStoreServing({ [`${PAGE}.html`]: "the page" }),
        edge,
        fetch: (async (input: Request) => {
          forwarded = input.url;
          return new Response("from ext");
        }) as unknown as typeof fetch,
      } as Partial<RouteDeps>),
    );

    expect(forwarded).toBe("https://ext.example.com/b/?q=1");
    expect(await res.text()).toBe("from ext");
  });

  it.each([`/b/`, "/b"])(
    "leaves a middleware redirect to %s exactly as authored",
    async (location) => {
      const edge = middlewareInvoker(
        `async () => new Response(null, {
           status: 307,
           headers: { location: "${location}" },
         })`,
      );

      const res = await serve(
        get(`${PAGE}/`),
        pageDeps({ trailingSlash: true, edge }),
      );

      expect(res.status).toBe(307);
      expect(res.headers.get("location")).toBe(location);
    },
  );

  describe("a request about to be redirected", () => {
    it("never reaches middleware", async () => {
      const { edge, calls } = counted(echo());
      const res = await serve(get(PAGE), pageDeps({ trailingSlash: true, edge }));

      expect(res.status).toBe(308);
      expect(res.headers.get("location")).toBe(`${PAGE}/`);
      expect(calls()).toBe(0);
    });

    it("is not answered by a middleware that would have taken it", async () => {
      const { edge, calls } = counted(
        middlewareInvoker(`async () => new Response("from middleware", { status: 200 })`),
      );
      const res = await serve(get(PAGE), pageDeps({ trailingSlash: true, edge }));

      expect(res.status).toBe(308);
      expect(calls()).toBe(0);
    });
  });

  describe("skipTrailingSlashRedirect", () => {
    it.each([
      [true, `${PAGE}/`],
      [true, PAGE],
      [false, `${PAGE}/`],
      [false, PAGE],
    ])("under trailingSlash: %s, shows middleware %s as requested", async (
      trailingSlash,
      path,
    ) => {
      const res = await serve(
        get(path),
        pageDeps({ trailingSlash, skipTrailingSlashRedirect: true }),
      );

      expect(res.status).toBe(200);
      expect(res.headers.get("req-url-path")).toBe(path);
      expect(await res.text()).toBe("the page");
    });

    it("still shows a data request as its page", async () => {
      const res = await serve(
        get(`/_next/data/t${PAGE}.json`, { "x-nextjs-data": "1" }),
        pageDeps({ trailingSlash: true, skipTrailingSlashRedirect: true }),
      );

      expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    });
  });

  describe("skipMiddlewareUrlNormalize", () => {
    it("shows middleware the canonical form the client sent", async () => {
      const res = await serve(
        get(`${PAGE}/`),
        pageDeps({ trailingSlash: true, skipMiddlewareUrlNormalize: true }),
      );

      expect(res.status).toBe(200);
      expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    });

    it("shows middleware a slash-free form the 308 no longer takes away", async () => {
      const res = await serve(
        get(PAGE),
        pageDeps({
          trailingSlash: true,
          skipMiddlewareUrlNormalize: true,
          skipTrailingSlashRedirect: true,
        }),
      );

      expect(res.status).toBe(200);
      expect(res.headers.get("req-url-path")).toBe(PAGE);
    });

    it.each([true, false])(
      "shows a data request as the data URL under trailingSlash: %s",
      async (trailingSlash) => {
        const path = `/_next/data/t${PAGE}.json`;
        const res = await serve(
          get(path, { "x-nextjs-data": "1" }),
          pageDeps({ trailingSlash, skipMiddlewareUrlNormalize: true }),
        );

        expect(res.headers.get("req-url-path")).toBe(path);
        expect(res.headers.get("x-matched-path")).toBe(path);
      },
    );
  });
});

describe("the variables a deployment declares", () => {
  const GO_ENVELOPE = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=";
  const GO_SEALED =
    "WnCHaOVOjB8oK/58SKZv0K2RggIVKRExHOoE8aOhDN5fsE1/ykforc+CJza3Ybm8JMnr3TJRBvuJ6b+ZfvNMf+hh6dMuz39GTxr2W6pURmg3NoPiQXUaeBc=";
  const GO_VALUES = { STRIPE_API_KEY: "sk-live-abc", WEBHOOK_SECRET: "whsec-xyz" };

  const NONCE_BYTES = 12;
  const TAG_BYTES = 16;

  function bytesOf(base64: string): Uint8Array {
    const binary = atob(base64);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    return bytes;
  }

  async function seal(envelope: string, plaintext: string): Promise<Uint8Array> {
    const key = await crypto.subtle.importKey(
      "raw",
      bytesOf(envelope),
      { name: "AES-GCM" },
      false,
      ["encrypt"],
    );
    const nonce = crypto.getRandomValues(new Uint8Array(NONCE_BYTES));
    const ciphertext = new Uint8Array(
      await crypto.subtle.encrypt(
        { name: "AES-GCM", iv: nonce },
        key,
        new TextEncoder().encode(plaintext),
      ),
    );
    const framed = new Uint8Array(nonce.length + ciphertext.length);
    framed.set(nonce);
    framed.set(ciphertext, nonce.length);
    return framed;
  }

  const DUMP = `async () =>
     new Response(JSON.stringify(Object.fromEntries(Object.entries(process.env))))`;

  const COUNTER = `(() => {
     let served = 0
     return async () => new Response(String(++served))
   })()`;

  interface VarsBundle {
    handler?: string;
    bundleEnv?: Record<string, string>;
    variables?: EdgeVariables;
    sealed?: Uint8Array;
    cache?: EdgeCacheBinding;
    id?: string;
  }

  function varsInvoker(bundle: VarsBundle): {
    edge: EdgeInvoker;
    reads: string[];
  } {
    const seq = bundles++;
    const prefix = `edge/vars/${seq}`;
    const bundleKey = `${prefix}/bundle.json`;
    const entries = { e: { chunks: ["c/0.js"], handlerExport: "handler" } };
    const json = JSON.stringify({
      version: 2,
      mainModule: "main.js",
      shim: shimFor(entries),
      chunks: { "c/0.js": chunkFor("e", bundle.handler ?? DUMP) },
      env: bundle.bundleEnv ?? { __NEXT_BUILD_ID: "t" },
    });

    const reads: string[] = [];
    const store: EdgeObjectStore = {
      async get(key) {
        reads.push(key);
        if (key === bundleKey) return storedJson(json);
        const sealed = bundle.sealed;
        if (key === `${prefix}/sealed.bin` && sealed) {
          return {
            text: async () => "",
            arrayBuffer: async () =>
              sealed.buffer.slice(
                sealed.byteOffset,
                sealed.byteOffset + sealed.byteLength,
              ),
          };
        }
        return null;
      },
    };

    const edge = createEdgeInvoker(
      env.LOADER,
      {
        bundleKey,
        id: bundle.id ?? `vars-${seq}`,
        compatDate: "2026-03-10",
        compatFlags: ["nodejs_compat"],
      },
      store,
      bundle.cache,
      bundle.variables,
    );
    return { edge, reads };
  }

  const workerEnv = async (edge: EdgeInvoker) =>
    JSON.parse(await (await edge("e", new Request("https://x/"))).text()) as
      Record<string, string>;

  it("hands a plain value to the worker under its own name", async () => {
    const { edge } = varsInvoker({ variables: { env: { API_URL: "https://api" } } });

    expect(await workerEnv(edge)).toMatchObject({
      API_URL: "https://api",
      __NEXT_BUILD_ID: "t",
    });
  });

  it("lets a declared value win over the bundle's own env", async () => {
    const { edge } = varsInvoker({
      bundleEnv: { __NEXT_BUILD_ID: "t", SHARED: "from-bundle" },
      variables: { env: { SHARED: "from-record" } },
    });

    expect((await workerEnv(edge)).SHARED).toBe("from-record");
  });

  it("keeps the names it owns for itself", async () => {
    const { edge } = varsInvoker({
      cache: { rpc: remoteStub(), scope: "prod/p/app/b1" },
      variables: {
        env: { OCEL_CACHE_SCOPE: "hijacked", OCEL_CACHE_RPC: "hijacked" },
      },
    });

    const values = await workerEnv(edge);
    expect(values.OCEL_CACHE_SCOPE).toBe("prod/p/app/b1");
    expect(values.OCEL_CACHE_RPC).not.toBe("hijacked");
  });

  it("unseals what the origin sealed in Go, prefixed and never bare", async () => {
    const sealed = bytesOf(GO_SEALED);
    expect(sealed.length).toBe(
      NONCE_BYTES + JSON.stringify(GO_VALUES).length + TAG_BYTES,
    );

    const { edge } = varsInvoker({
      variables: { envelope: GO_ENVELOPE },
      sealed,
    });

    const values = await workerEnv(edge);
    expect(values.OCEL_VAR_STRIPE_API_KEY).toBe("sk-live-abc");
    expect(values.OCEL_VAR_WEBHOOK_SECRET).toBe("whsec-xyz");
    expect(values.STRIPE_API_KEY).toBeUndefined();
    expect(values.WEBHOOK_SECRET).toBeUndefined();
  });

  it("unseals a nonce-prefixed AES-GCM payload of its own making", async () => {
    const values = { TOKEN: "t0ken" };
    const sealed = await seal(GO_ENVELOPE, JSON.stringify(values));
    expect(sealed.length).toBe(
      NONCE_BYTES + JSON.stringify(values).length + TAG_BYTES,
    );

    const { edge } = varsInvoker({ variables: { envelope: GO_ENVELOPE }, sealed });

    expect((await workerEnv(edge)).OCEL_VAR_TOKEN).toBe("t0ken");
  });

  it("names a non-JSON payload without quoting what it decrypted", async () => {
    const plaintext = "sk-live-abc is not JSON";
    const sealed = await seal(GO_ENVELOPE, plaintext);

    const { edge } = varsInvoker({ variables: { envelope: GO_ENVELOPE }, sealed });

    const failure = await edge("e", new Request("https://x/")).then(
      () => null,
      (error: Error) => error,
    );
    expect(failure?.message).toBe("ocel: sealed edge variables are not JSON");
  });

  it("refuses to load when the envelope names bytes the store has not got", async () => {
    const { edge } = varsInvoker({ variables: { envelope: GO_ENVELOPE } });

    await expect(edge("e", new Request("https://x/"))).rejects.toThrow(
      /no sealed edge variables at .*\/sealed\.bin/,
    );
  });

  it("never reads the sealed object when nothing was sealed", async () => {
    const { edge, reads } = varsInvoker({ variables: { env: { A: "one" } } });

    await workerEnv(edge);
    expect(reads.filter((key) => key.endsWith("/sealed.bin"))).toEqual([]);
  });

  it("reloads the isolate when only the values changed", async () => {
    const deployment = (valueFingerprint: string) =>
      varsInvoker({
        handler: COUNTER,
        id: "shared-bundle-vars",
        cache: { rpc: remoteStub(), scope: "prod/p/app/b1" },
        variables: { valueFingerprint },
      }).edge;
    const served = async (edge: EdgeInvoker) =>
      (await edge("e", new Request("https://x/"))).text();

    expect(await served(deployment("fp-1"))).toBe("1");
    expect(await served(deployment("fp-1"))).toBe("2");
    expect(await served(deployment("fp-2"))).toBe("1");
  });
});
