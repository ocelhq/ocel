import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { dispatchResult, serve, type RouteDeps } from "../src/index";
import { createEdgeInvoker, type EdgeInvoker } from "../src/edge";
import type { AssetBucket } from "../src/assets";
import type { ObjectStoreReader } from "../src/tag-clock";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    LOADER: WorkerLoader;
  }
}

interface EdgeEntry {
  chunks: string[];
  handlerExport: string;
}

// The shim the adapter emits into every bundle, verbatim: entry table inlined,
// process.env populated before the first chunk import, entry key read off
// ctx.props.
function shimFor(entries: Record<string, EdgeEntry>): string {
  return `import { AsyncLocalStorage } from "node:async_hooks"

const ENTRIES = ${JSON.stringify(entries)}

export default {
  async fetch(request, env, ctx) {
    globalThis.AsyncLocalStorage ??= AsyncLocalStorage
    globalThis.process ??= { env: {} }
    Object.assign(globalThis.process.env, env)
    const k = ctx.props.entryKey
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

// A chunk in the shape Turbopack emits one: a classic script that registers its
// entry on globalThis._ENTRIES rather than exporting anything.
function chunkFor(entryKey: string, handler: string): string {
  return `globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = { handler: ${handler} }
`;
}

// A chunk that reaches a Node builtin the way Turbopack's edge chunks do:
// `require` at module scope, synchronously, through its externalRequire helper.
// Next compiles every edge entry that touches AsyncLocalStorage or Buffer this
// way, so this is the shape a real bundle carries, not an exotic one.
function nodeRequireChunkFor(entryKey: string): string {
  return `const { Buffer } = require("node:buffer")
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () => new Response(Buffer.from("required").toString()),
}
`;
}

// The loader keys its compiled worker on the record id, so every bundle gets a
// fresh one — otherwise the second test to use the same id would run the first
// test's code.
let bundles = 0;

// invokerFor builds a real dynamic-worker invoker over a synthetic bundle: one
// chunk per entry, served out of an in-memory store shaped like the R2 binding.
function invokerFor(
  handlers: Record<string, string>,
  chunkSource: (entryKey: string, handler: string) => string = chunkFor,
): EdgeInvoker {
  const chunks: Record<string, string> = {};
  const entries: Record<string, EdgeEntry> = {};
  let next = 0;
  for (const [entryKey, handler] of Object.entries(handlers)) {
    const id = `c/${next++}.js`;
    chunks[id] = chunkSource(entryKey, handler);
    entries[entryKey] = { chunks: [id], handlerExport: "handler" };
  }

  const json = JSON.stringify({
    version: 1,
    mainModule: "main.js",
    shim: shimFor(entries),
    chunks,
    wasm: {},
    env: { __NEXT_BUILD_ID: "t" },
    entries,
  });

  const seq = bundles++;
  const bundleKey = `edge/${seq}/bundle.json`;
  const store: ObjectStoreReader = {
    async get(key) {
      return key === bundleKey ? { text: async () => json } : null;
    },
  };
  return createEdgeInvoker(
    env.LOADER,
    {
      bundleKey,
      id: `edge-${seq}`,
      compatDate: "2026-03-10",
      compatFlags: ["nodejs_compat"],
    },
    store,
  );
}

// An invoker over a bundle written in a layout this worker does not know. The
// worker script is frozen and outlives its deployments (ADR 0002), so it can be
// handed one a later adapter wrote.
function futureBundleInvoker(): EdgeInvoker {
  const seq = bundles++;
  const bundleKey = `edge/${seq}/bundle.json`;
  const json = JSON.stringify({
    version: 2,
    mainModule: "main.js",
    shim: shimFor({}),
    chunks: {},
    entries: {},
  });
  return createEdgeInvoker(
    env.LOADER,
    { bundleKey, id: `edge-future-${seq}`, compatDate: "2026-03-10" },
    { async get(key) { return key === bundleKey ? { text: async () => json } : null; } },
  );
}

// An invoker whose bundle is not in the store: the loader callback throws.
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

// Counts invocations without displacing the real loader underneath, so a test
// can assert an entry was never reached rather than inferring it from output.
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
    assetStore: assetStoreServing({}),
    ...overrides,
  } as RouteDeps;
}

// A manifest serving one static file, so a middleware that lets the request
// through has something concrete to fall through to.
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

// gated serves /static.txt through a middleware with the given matcher config
// and reports both whether the entry ran and what the client got.
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
    // resolveRoutes calls invokeMiddleware unconditionally — it has no matcher
    // field at all — so without this gate every static asset spins up middleware.
    const { res, calls } = await gated([{ sourceRegex: "^/dashboard$" }]);

    expect(calls).toBe(0);
    expect(await res.text()).toBe("the-file");
    expect(res.headers.get("x-mw-ran")).toBe(null);
  });

  it("invokes middleware on every path when the matcher list is absent", async () => {
    // Next's semantics for a bare middleware.ts with no `config` export: no
    // matchers means run on everything, not never run.
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

describe("middleware responses", () => {
  it("returns the middleware's own body and status when it answers the request", async () => {
    // resolveRoutes reports only `middlewareResponded`, with no status, headers
    // or body — so a NextResponse.json() used to reach the client as a blank 200.
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
    // responseToMiddlewareResult puts the rewrite destination back onto the
    // response headers after its own exclusion list, and resolveRoutes copies
    // those into resolvedHeaders — so applying them verbatim published the
    // internal path every rewrite resolves to.
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
    // resolveRoutes returns a middleware redirect as bare resolvedHeaders with
    // no resolvedPathname, so this used to fall through to a 404 — and
    // Response.redirect could not have carried the cookie anyway.
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
    // Middleware and the origin forward each need their own stream over the
    // buffered bytes; routing used to be handed the live request body, so an
    // app calling request.json() in middleware starved the origin.
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
    // Fail closed: an auth middleware that could not run must not let the page
    // it protects be served.
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
        return entry === undefined
          ? null
          : { text: async () => JSON.stringify(entry) };
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
            entryKey: "middleware_app/edge-blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      // No Function URL at all: the parent renders on the edge.
      functionUrls: {},
      edge,
      cache: {
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: () => {},
      },
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
