import { env } from "cloudflare:test";
import { edgeBundleVersion } from "@platform/edge-contract/edge-bundle";
import { describe, expect, it } from "vitest";
import {
  createEdgeInvoker,
  type EdgeEntryKind,
  type EdgeInvoker,
  type EdgeObjectStore,
  type EdgeVariables,
  ownBundleKey,
} from "../src/edge";
import type { ServeFetch } from "../src/index";
import { chunkFor, countingLoader, edgeBundle } from "./edge-bundle";
import {
  EMPTY_ROUTES,
  type NextRelease,
  servedNextRelease,
  UNTOKENED_PAGES_PREFIX,
} from "./next-release";

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

let bundles = 0;

function invokerFor(
  handlers: Record<string, string>,
  chunkSource: (entryKey: string, handler: string) => string = chunkFor,
  id?: string,
  scope?: string,
): EdgeInvoker {
  const json = edgeBundle(handlers, { chunkSource });
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
      loaderId: id ?? `edge-${seq}`,
      compatDate: "2026-03-10",
      compatFlags: ["nodejs_compat"],
    },
    store,
    { scope },
  );
}

function missingBundle(): NextRelease["record"] {
  const seq = bundles++;
  return {
    edge: {
      code: {
        bundleKey: `bundles/p1/prod/web/r9${String(seq).padStart(7, "0")}/bundle.json`,
        loaderId: `edge-missing-${seq}`,
        compatDate: "2026-03-10",
        compatFlags: [],
      },
    },
  };
}

const MIDDLEWARE_KEY = "middleware_middleware";

function middlewareBundle(handler: string): string {
  return edgeBundle({ [MIDDLEWARE_KEY]: handler });
}

const PASSTHROUGH = middlewareBundle(
  `async () => new Response(null, { headers: { "x-middleware-next": "1", "x-mw-ran": "1" } })`,
);

function staticRelease(middleware: unknown, over: NextRelease = {}): ServeFetch {
  return servedNextRelease({
    edgeBundle: PASSTHROUGH,
    ...over,
    table: {
      pathnames: ["/static.txt"],
      dispatch: { "/static.txt": { kind: "static" } },
      middleware: middleware as never,
    },
    assets: { "/static.txt": "the-file" },
  });
}

const staticFile = () => new Request("https://app.example/static.txt");

async function gated(
  matchers: unknown,
  init?: RequestInit,
): Promise<{ res: Response; calls: number }> {
  const { loader, invocations } = countingLoader();
  const middleware = { entryKey: MIDDLEWARE_KEY, ...(matchers !== undefined && { matchers }) };
  const res = await staticRelease(middleware, { loader })(
    new Request("https://app.example/static.txt", init),
  );
  return { res, calls: invocations.length };
}

describe("ownBundleKey", () => {
  it.each([
    ["bundles/p1/prod/web/r00000001/bundle.json", true],
    ["bundles/p1/preview/web/r00000001/bundle.json", true],
    ["bundles/p2/prod/web/r00000001/bundle.json", false],
    ["bundles/p1/prod/admin/r00000001/bundle.json", false],
    ["bundles/p1/prod/web/r00000001/sealed.bin", false],
    ["assets/p1/prod/web/r00000001/bundle.json", false],
    ["bundles/p1/prod/web/../../p2/r00000001/bundle.json", false],
    ["bundles/p1/prod/web/r00000001/bundle.json/extra", false],
    ["", false],
  ])("%s is p1/web's own: %s", (key, own) => {
    expect(ownBundleKey(key, "p1", "web")).toBe(own);
  });
});

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
    expect((await gated(matchers, { headers: { "x-gate": "on" } })).calls).toBe(1);
  });

  it("honours a matcher's missing condition", async () => {
    const matchers = [
      {
        sourceRegex: "^/static\\.txt$",
        missing: [{ type: "header", key: "x-skip" }],
      },
    ];

    expect((await gated(matchers)).calls).toBe(1);
    expect((await gated(matchers, { headers: { "x-skip": "1" } })).calls).toBe(0);
  });
});

describe("middleware matchers under i18n", () => {
  const I18N = { locales: ["en", "fr"], defaultLocale: "en" };

  function localeRelease(matchers: unknown, loader?: WorkerLoader): ServeFetch {
    const pathnames = [
      "/en/foo",
      "/fr/foo",
      "/en/bar",
      "/fr/bar",
      "/_next/data/t/en/foo.json",
      "/_next/data/t/fr/foo.json",
    ];
    return servedNextRelease({
      table: {
        i18n: I18N,
        pathnames,
        dispatch: Object.fromEntries(pathnames.map((path) => [path, { kind: "static" }])),
        middleware: { entryKey: MIDDLEWARE_KEY, matchers } as never,
      },
      assets: {
        "/en/foo.html": "the en page",
        "/fr/foo.html": "the fr page",
        "/en/bar.html": "the en bar page",
        "/fr/bar.html": "the fr bar page",
        "/_next/data/t/en/foo.json": '{"at":"en/foo"}',
        "/_next/data/t/fr/foo.json": '{"at":"fr/foo"}',
        "/en/404.html": "not found",
      },
      edgeBundle: PASSTHROUGH,
      loader,
    });
  }

  it("matches an unprefixed default-locale path against a mandatory locale group", async () => {
    const { loader, invocations } = countingLoader();
    const res = await localeRelease(
      [{ sourceRegex: "^/(?:en|fr)/foo$" }],
      loader,
    )(new Request("https://app.example/foo"));

    expect(res.headers.get("x-mw-ran")).toBe("1");
    expect(invocations).toHaveLength(1);
    expect(await res.text()).toBe("the en page");
  });

  it("still leaves a genuinely unmatched path alone", async () => {
    const { loader, invocations } = countingLoader();
    const res = await localeRelease(
      [{ sourceRegex: "^/(?:en|fr)/foo$" }],
      loader,
    )(new Request("https://app.example/bar"));

    expect(res.headers.get("x-mw-ran")).toBe(null);
    expect(invocations).toHaveLength(0);
    expect(await res.text()).toBe("the en bar page");
  });

  it("resolves a _next/data page URL with no locale of its own", async () => {
    const res = await localeRelease(undefined)(
      new Request("https://app.example/_next/data/t/foo.json", {
        headers: { "x-nextjs-data": "1" },
      }),
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/_next/data/t/en/foo.json");
    expect(await res.text()).toBe('{"at":"en/foo"}');
  });
});

describe("middleware responses", () => {
  const answering = (handler: string) =>
    staticRelease({ entryKey: MIDDLEWARE_KEY }, { edgeBundle: middlewareBundle(handler) });

  it("returns the middleware's own body and status when it answers the request", async () => {
    const res = await answering(
      `async () => new Response(JSON.stringify({ error: "nope" }), {
         status: 401,
         headers: { "content-type": "application/json" },
       })`,
    )(staticFile());

    expect(res.status).toBe(401);
    expect(res.headers.get("content-type")).toBe("application/json");
    expect(await res.json()).toEqual({ error: "nope" });
  });

  it("strips the x-middleware-* control headers off a middleware response", async () => {
    const res = await answering(
      `async () => new Response("denied", {
         status: 403,
         headers: { "x-middleware-set-cookie": "sid=1", "x-keep": "1" },
       })`,
    )(staticFile());

    expect(res.status).toBe(403);
    expect(res.headers.get("x-middleware-set-cookie")).toBe(null);
    expect(res.headers.get("x-keep")).toBe("1");
  });

  it.each([204, 304])("returns a middleware's %i, which has no body at all", async (status) => {
    const res = await answering(`async () => new Response(null, { status: ${status} })`)(
      staticFile(),
    );

    expect(res.status).toBe(status);
  });

  function functionRelease(
    path: string,
    handler: string,
    fetch: typeof globalThis.fetch,
  ): ServeFetch {
    return servedNextRelease({
      table: {
        dispatch: { [path]: { kind: "function", id: path } },
        middleware: { entryKey: MIDDLEWARE_KEY },
      },
      record: { functionUrls: { [path]: "https://fn.example.com" } },
      edgeBundle: middlewareBundle(handler),
      fetch,
    });
  }

  it("keeps the x-middleware-* control headers off a rewritten response", async () => {
    const res = await functionRelease(
      "/dest",
      `async () => new Response(null, {
         headers: {
           "x-middleware-rewrite": "/dest",
           "x-middleware-request-x-tenant": "acme",
           "x-keep": "1",
         },
       })`,
      (async () => new Response("origin")) as unknown as typeof fetch,
    )(new Request("https://app.example/from"));

    expect(await res.text()).toBe("origin");
    expect(res.headers.get("x-keep")).toBe("1");
    expect([...res.headers.keys()].filter((n) => n.startsWith("x-middleware-"))).toEqual([]);
  });

  it("redirects with the middleware's Set-Cookie intact", async () => {
    const res = await answering(
      `async () => new Response(null, {
         status: 307,
         headers: { location: "/login", "set-cookie": "sid=abc; Path=/" },
       })`,
    )(staticFile());

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/login");
    expect(res.headers.getSetCookie()).toEqual(["sid=abc; Path=/"]);
  });

  it("forwards a middleware request-header override to the origin", async () => {
    let captured: Request | undefined;

    await functionRelease(
      "/api/me",
      `async () => new Response(null, {
         headers: {
           "x-middleware-next": "1",
           "x-middleware-override-headers": "x-user",
           "x-middleware-request-x-user": "alice",
         },
       })`,
      (async (req: Request) => {
        captured = req;
        return new Response("ok");
      }) as unknown as typeof fetch,
    )(new Request("https://app.example/api/me"));

    expect(captured?.headers.get("x-user")).toBe("alice");
  });

  it("leaves the origin a readable body after middleware consumed one", async () => {
    let captured: Request | undefined;

    const res = await functionRelease(
      "/api/me",
      `async (request) => new Response(null, {
         headers: { "x-middleware-next": "1", "x-mw-body": await request.text() },
       })`,
      (async (req: Request) => {
        captured = req;
        return new Response("ok");
      }) as unknown as typeof fetch,
    )(
      new Request("https://app.example/api/me", {
        method: "POST",
        body: "hello=world",
      }),
    );

    expect(res.headers.get("x-mw-body")).toBe("hello=world");
    expect(await captured?.text()).toBe("hello=world");
  });
});

describe("resolvedHeaders", () => {
  it("applies a next.config headers() rule to the served response", async () => {
    const res = await servedNextRelease({
      table: {
        routes: {
          ...EMPTY_ROUTES,
          onMatch: [
            {
              sourceRegex: "^/static\\.txt$",
              headers: { "x-frame-options": "DENY" },
            },
          ],
        },
        dispatch: { "/static.txt": { kind: "static" } },
      },
      assets: { "/static.txt": "the-file" },
    })(staticFile());

    expect(res.headers.get("x-frame-options")).toBe("DENY");
    expect(await res.text()).toBe("the-file");
  });
});

describe("edge dispatch", () => {
  const EDGE_KEY = "middleware_app/edge";

  const edgeRoute = (over: NextRelease) =>
    servedNextRelease({
      ...over,
      table: { dispatch: { "/edge": { kind: "edge", entryKey: EDGE_KEY } } },
    })(new Request("https://app.example/edge"));

  const servedBy = (handler: string, options: Parameters<typeof edgeBundle>[1] = {}) =>
    edgeRoute({ edgeBundle: edgeBundle({ [EDGE_KEY]: handler }, options) });

  it("invokes an edge route's entry through the loader", async () => {
    const res = await servedBy(
      `async (request) =>
         new Response("from-edge:" + new URL(request.url).pathname + ":" + process.env.__NEXT_BUILD_ID)`,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-edge:/edge:t");
  });

  it("invokes an entry whose chunk requires a Node builtin", async () => {
    const res = await servedBy("", { chunkSource: (entryKey) => nodeRequireChunkFor(entryKey) });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("required");
  });

  it("invokes an entry whose chunk reads the AsyncLocalStorage global", async () => {
    const res = await servedBy("", {
      chunkSource: (entryKey) => `if (typeof AsyncLocalStorage !== "function") {
  throw new Error("Invariant: AsyncLocalStorage accessed in runtime where it is not available")
}
const store = new AsyncLocalStorage()
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () => store.run("scoped", () => new Response(store.getStore())),
}
`,
    });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("scoped");
  });

  it("publishes the entry key on a global before an entry's chunks evaluate", async () => {
    const res = await servedBy("", {
      chunkSource: (entryKey) => `const seenAtModuleScope = globalThis.__OCEL_EDGE_ENTRY
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () => new Response(String(seenAtModuleScope)),
}
`,
    });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe(EDGE_KEY);
  });

  it("answers a blob: fetch from the bundle's assets, byte for byte", async () => {
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe]);
    const res = await servedBy(
      `async () =>
         new Response(await (await fetch(new URL("blob:server/edge/assets/pic.png"))).arrayBuffer())`,
      { assets: { "server/edge/assets/pic.png": bytes } },
    );

    expect(res.status).toBe(200);
    expect(new Uint8Array(await res.arrayBuffer())).toEqual(bytes);
  });

  it("hands a fetch the asset table does not name to the platform", async () => {
    const res = await servedBy(
      `async () => {
         const res = await fetch("data:text/plain,from-the-platform")
         return new Response(res.status + ":" + (await res.text()))
       }`,
      { assets: { "server/edge/assets/pic.png": new Uint8Array([1, 2, 3]) } },
    );

    expect(await res.text()).toBe("200:from-the-platform");
  });

  it("hands an unknown blob: name to the platform, which rejects it", async () => {
    const res = await servedBy(
      `async () => {
         try {
           await fetch("blob:server/edge/assets/absent.png")
           return new Response("answered-from-table")
         } catch (e) {
           return new Response(e.name + "|" + e.message)
         }
       }`,
      { assets: { "server/edge/assets/pic.png": new Uint8Array([1, 2, 3]) } },
    );

    expect(await res.text()).toBe(
      "TypeError|Fetch API cannot load: blob:server/edge/assets/absent.png",
    );
  });

  it("resolves an asset whose name the URL constructor percent-encodes", async () => {
    const bytes = new Uint8Array([0xde, 0xad, 0xbe, 0xef]);
    const res = await servedBy(
      `async () =>
         new Response(await (await fetch(new URL("blob:server/edge/assets/café.png"))).arrayBuffer())`,
      { assets: { "server/edge/assets/café.png": bytes } },
    );

    expect(res.status).toBe(200);
    expect(new Uint8Array(await res.arrayBuffer())).toEqual(bytes);
  });

  it("serves an entry from a bundle containing an asset that is not valid UTF-8", async () => {
    const res = await edgeRoute({
      edgeBundle: edgeBundle(
        {
          "middleware_app/other": `async () => new Response("other-entry")`,
          [EDGE_KEY]: `async () => new Response("edge-entry")`,
        },
        {
          assets: {
            "server/edge/assets/pic.png": new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0xff, 0xfe]),
          },
        },
      ),
    });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("edge-entry");
  });

  it("returns 500 when the bundle cannot be read", async () => {
    const res = await edgeRoute({ record: missingBundle() });

    expect(res.status).toBe(500);
  });

  it("returns 500 for a bundle whose version this worker cannot read", async () => {
    const res = await edgeRoute({ edgeBundle: edgeBundle({}, { version: edgeBundleVersion - 1 }) });

    expect(res.status).toBe(500);
  });

  it("returns 500 rather than routing on when middleware cannot be invoked", async () => {
    const res = await staticRelease(
      { entryKey: MIDDLEWARE_KEY },
      { record: missingBundle() },
    )(staticFile());

    expect(res.status).toBe(500);
    expect(await res.text()).not.toContain("the-file");
  });

  it("returns 500 when middleware itself throws", async () => {
    const res = await staticRelease(
      { entryKey: MIDDLEWARE_KEY },
      { edgeBundle: middlewareBundle(`async () => { throw new Error("boom") }`) },
    )(staticFile());

    expect(res.status).toBe(500);
  });
});

describe("edge-parented prerenders", () => {
  const renderer = edgeBundle({
    "middleware_app/edge-blog": `async () =>
       new Response("<html>rendered-on-edge</html>", {
         status: 200,
         headers: { "cache-control": "s-maxage=60" },
       })`,
  });

  it("invokes the edge entry with no Function URL bound when its release names no token to cache it under", async () => {
    const res = await servedNextRelease({
      table: {
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
      record: { pagesPrefix: UNTOKENED_PAGES_PREFIX },
      edgeBundle: renderer,
    })(new Request("https://app.example/edge-blog"));

    expect(await res.text()).toBe("<html>rendered-on-edge</html>");
  });

  it("still 502s a prerender with neither a Function URL nor an edge entry", async () => {
    const res = await servedNextRelease({
      table: { dispatch: { "/blog": { kind: "prerender", id: "/blog", config: {} } } },
      record: { pagesPrefix: UNTOKENED_PAGES_PREFIX },
    })(new Request("https://app.example/blog"));

    expect(res.status).toBe(502);
  });
});

describe("the loaded isolate's identity", () => {
  const servedCount = (entryKey: string) => `let served = 0
globalThis._ENTRIES ??= {}
globalThis._ENTRIES[${JSON.stringify(entryKey)}] = {
  handler: async () =>
    new Response(++served + ":" + globalThis.__OCEL_EDGE_ENTRY),
}
`;

  const deployment = (id: string, scope: string) => invokerFor({ e: "" }, servedCount, id, scope);

  const serve = async (edge: EdgeInvoker, kind?: EdgeEntryKind) =>
    (await edge("e", new Request("https://x/"), kind)).text();

  it("reuses one isolate across the requests of one deployment", async () => {
    const prod = deployment("shared-bundle-a", "prod/p/app/b1");
    const same = deployment("shared-bundle-a", "prod/p/app/b1");

    expect(await serve(prod)).toBe("1:e");
    expect(await serve(same)).toBe("2:e");
  });

  it("separates two deployments that share a bundle but not a scope", async () => {
    const prod = deployment("shared-bundle-b", "prod/p/app/b1");
    const preview = deployment("shared-bundle-b", "preview/p/app/b1");

    expect(await serve(prod)).toBe("1:e");
    expect(await serve(preview)).toBe("1:e");
  });

  it("keeps a middleware entry off the isolate a page entry already warmed", async () => {
    const bundle = deployment("shared-bundle-c", "prod/p/app/b1");

    expect(await serve(bundle)).toBe("1:e");
    expect(await serve(bundle, "middleware")).toBe("1:e");
  });

  it("reuses the middleware isolate across its own requests", async () => {
    const bundle = deployment("shared-bundle-d", "prod/p/app/b1");

    expect(await serve(bundle, "middleware")).toBe("1:e");
    expect(await serve(bundle, "middleware")).toBe("2:e");
    expect(await serve(bundle)).toBe("1:e");
  });
});

describe("the URL middleware is handed", () => {
  const PAGE = "/send-url";

  interface EdgeScenario {
    bundle: string;
    loader?: WorkerLoader;
  }

  const middlewareAnswering = (handler: string): EdgeScenario => ({
    bundle: middlewareBundle(handler),
  });

  const echo = () =>
    middlewareAnswering(
      `async (request) => new Response(null, {
         headers: {
           "x-middleware-next": "1",
           "req-url-path": new URL(request.url).pathname,
         },
       })`,
    );

  function counted(edge: EdgeScenario): { edge: EdgeScenario; calls: () => number } {
    const { loader, invocations } = countingLoader();
    return { edge: { ...edge, loader }, calls: () => invocations.length };
  }

  const serve = (request: Request, release: ServeFetch) => release(request);

  interface Scenario {
    trailingSlash?: boolean;
    skipTrailingSlashRedirect?: boolean;
    skipMiddlewareUrlNormalize?: boolean;
    matchers?: unknown;
    edge?: EdgeScenario;
  }

  function pageRelease(scenario: Scenario): ServeFetch {
    const dataPaths = [`/_next/data/t${PAGE}.json`, "/_next/data/t/index.json"];
    const pathnames = [PAGE, "/", ...dataPaths];
    const edge = scenario.edge ?? echo();
    return servedNextRelease({
      table: {
        trailingSlash: scenario.trailingSlash,
        skipTrailingSlashRedirect: scenario.skipTrailingSlashRedirect,
        skipMiddlewareUrlNormalize: scenario.skipMiddlewareUrlNormalize,
        pathnames,
        dispatch: Object.fromEntries(pathnames.map((path) => [path, { kind: "static" }])),
        middleware: {
          entryKey: MIDDLEWARE_KEY,
          ...(scenario.matchers !== undefined && { matchers: scenario.matchers as never }),
        },
      },
      assets: {
        [`${PAGE}.html`]: "the page",
        "/.html": "the root",
        "/404.html": "not found",
        ...Object.fromEntries(dataPaths.map((path) => [path, `{"at":"${path}"}`])),
      },
      edgeBundle: edge.bundle,
      loader: edge.loader,
    });
  }

  function get(path: string, headers: Record<string, string> = {}) {
    return new Request(`https://app.example${path}`, { redirect: "manual", headers });
  }

  it("hands middleware the slashed pathname under trailingSlash: true", async () => {
    const res = await serve(get(`${PAGE}/`), pageRelease({ trailingSlash: true }));

    expect(res.status).toBe(200);
    expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    expect(await res.text()).toBe("the page");
  });

  it("hands middleware the bare pathname under trailingSlash: false", async () => {
    const res = await serve(get(PAGE), pageRelease({}));

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
          pageRelease({ trailingSlash }),
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
          pageRelease({ trailingSlash }),
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
        pageRelease({ trailingSlash: true, matchers: [{ sourceRegex: "^/send-url/$" }], edge }),
      );

      expect(calls()).toBe(1);
      expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    });

    it("loses no match for one path-to-regexp wrote with the optional slash", async () => {
      const { edge, calls } = counted(echo());
      await serve(
        get(`${PAGE}/`),
        pageRelease({
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
        pageRelease({ trailingSlash: true, matchers: [{ sourceRegex: "^/other$" }], edge }),
      );

      expect(calls()).toBe(0);
      expect(await res.text()).toBe("the page");
    });
  });

  it("routes a canonical rewrite destination on its routing form, forwarding it slashed", async () => {
    const edge = middlewareAnswering(
      `async () => new Response(null, { headers: { "x-middleware-rewrite": "/b/" } })`,
    );

    const res = await serve(
      get(`${PAGE}/`),
      servedNextRelease({
        table: {
          trailingSlash: true,
          pathnames: [PAGE, "/b"],
          dispatch: { [PAGE]: { kind: "static" }, "/b": { kind: "function", id: "/b" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        assets: { [`${PAGE}.html`]: "the page", "/404.html": "not found" },
        record: { functionUrls: { "/b": "https://fn.example.com" } },
        edgeBundle: edge.bundle,
        fetch: (async (input: Request) =>
          new Response(new URL(input.url).pathname)) as unknown as typeof fetch,
      }),
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/b");
    expect(await res.text()).toBe("/b/");
  });

  it("forwards a rewrite to another origin byte-verbatim", async () => {
    const edge = middlewareAnswering(
      `async () => new Response(null, {
         headers: { "x-middleware-rewrite": "https://ext.example.com/b/?q=1" },
       })`,
    );
    let forwarded: string | undefined;

    const res = await serve(
      get(`${PAGE}/`),
      servedNextRelease({
        table: {
          trailingSlash: true,
          pathnames: [PAGE],
          dispatch: { [PAGE]: { kind: "static" } },
          middleware: { entryKey: MIDDLEWARE_KEY },
        },
        assets: { [`${PAGE}.html`]: "the page" },
        edgeBundle: edge.bundle,
        fetch: (async (input: Request) => {
          forwarded = input.url;
          return new Response("from ext");
        }) as unknown as typeof fetch,
      }),
    );

    expect(forwarded).toBe("https://ext.example.com/b/?q=1");
    expect(await res.text()).toBe("from ext");
  });

  it.each([`/b/`, "/b"])(
    "leaves a middleware redirect to %s exactly as authored",
    async (location) => {
      const edge = middlewareAnswering(
        `async () => new Response(null, {
           status: 307,
           headers: { location: "${location}" },
         })`,
      );

      const res = await serve(get(`${PAGE}/`), pageRelease({ trailingSlash: true, edge }));

      expect(res.status).toBe(307);
      expect(res.headers.get("location")).toBe(location);
    },
  );

  describe("a request about to be redirected", () => {
    it("never reaches middleware", async () => {
      const { edge, calls } = counted(echo());
      const res = await serve(get(PAGE), pageRelease({ trailingSlash: true, edge }));

      expect(res.status).toBe(308);
      expect(res.headers.get("location")).toBe(`${PAGE}/`);
      expect(calls()).toBe(0);
    });

    it("is not answered by a middleware that would have taken it", async () => {
      const { edge, calls } = counted(
        middlewareAnswering(`async () => new Response("from middleware", { status: 200 })`),
      );
      const res = await serve(get(PAGE), pageRelease({ trailingSlash: true, edge }));

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
    ])("under trailingSlash: %s, shows middleware %s as requested", async (trailingSlash, path) => {
      const res = await serve(
        get(path),
        pageRelease({ trailingSlash, skipTrailingSlashRedirect: true }),
      );

      expect(res.status).toBe(200);
      expect(res.headers.get("req-url-path")).toBe(path);
      expect(await res.text()).toBe("the page");
    });

    it("still shows a data request as its page", async () => {
      const res = await serve(
        get(`/_next/data/t${PAGE}.json`, { "x-nextjs-data": "1" }),
        pageRelease({ trailingSlash: true, skipTrailingSlashRedirect: true }),
      );

      expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    });
  });

  describe("skipMiddlewareUrlNormalize", () => {
    it("shows middleware the canonical form the client sent", async () => {
      const res = await serve(
        get(`${PAGE}/`),
        pageRelease({ trailingSlash: true, skipMiddlewareUrlNormalize: true }),
      );

      expect(res.status).toBe(200);
      expect(res.headers.get("req-url-path")).toBe(`${PAGE}/`);
    });

    it("shows middleware a slash-free form the 308 no longer takes away", async () => {
      const res = await serve(
        get(PAGE),
        pageRelease({
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
          pageRelease({ trailingSlash, skipMiddlewareUrlNormalize: true }),
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

  interface VariablesBundle {
    handler?: string;
    bundleEnv?: Record<string, string>;
    variables?: EdgeVariables;
    sealed?: Uint8Array;
    id?: string;
  }

  function variablesInvoker(bundle: VariablesBundle): {
    edge: EdgeInvoker;
    reads: string[];
  } {
    const seq = bundles++;
    const prefix = `edge/variables/${seq}`;
    const bundleKey = `${prefix}/bundle.json`;
    const entries = { e: { chunks: ["c/0.js"], handlerExport: "handler" } };
    const json = JSON.stringify({
      version: edgeBundleVersion,
      entries,
      chunks: { "c/0.js": chunkFor("e", bundle.handler ?? DUMP) },
      env: bundle.bundleEnv ?? { __NEXT_BUILD_ID: "t" },
      clientAssetSuffix: "",
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
              sealed.buffer.slice(sealed.byteOffset, sealed.byteOffset + sealed.byteLength),
          };
        }
        return null;
      },
    };

    const edge = createEdgeInvoker(
      env.LOADER,
      {
        bundleKey,
        loaderId: bundle.id ?? `variables-${seq}`,
        compatDate: "2026-03-10",
        compatFlags: ["nodejs_compat"],
      },
      store,
      bundle.variables,
    );
    return { edge, reads };
  }

  const workerEnv = async (edge: EdgeInvoker) =>
    JSON.parse(await (await edge("e", new Request("https://x/"))).text()) as Record<string, string>;

  it("hands a plain value to the worker under its own name", async () => {
    const { edge } = variablesInvoker({ variables: { env: { API_URL: "https://api" } } });

    expect(await workerEnv(edge)).toMatchObject({
      API_URL: "https://api",
      __NEXT_BUILD_ID: "t",
    });
  });

  it("hands the deployment url to an edge entry like any other plain value", async () => {
    const { edge } = variablesInvoker({
      variables: {
        env: { OCEL_URL: "https://shop.example", NEXT_PUBLIC_OCEL_URL: "https://shop.example" },
      },
    });

    expect(await workerEnv(edge)).toMatchObject({
      OCEL_URL: "https://shop.example",
      NEXT_PUBLIC_OCEL_URL: "https://shop.example",
    });
  });

  it("lets a declared value win over the bundle's own env", async () => {
    const { edge } = variablesInvoker({
      bundleEnv: { __NEXT_BUILD_ID: "t", SHARED: "from-bundle" },
      variables: { env: { SHARED: "from-record" } },
    });

    expect((await workerEnv(edge)).SHARED).toBe("from-record");
  });

  it("unseals what the origin sealed in Go, each value under its own name", async () => {
    const sealed = bytesOf(GO_SEALED);
    expect(sealed.length).toBe(NONCE_BYTES + JSON.stringify(GO_VALUES).length + TAG_BYTES);

    const { edge } = variablesInvoker({
      variables: { envelope: GO_ENVELOPE },
      sealed,
    });

    const values = await workerEnv(edge);
    expect(values.STRIPE_API_KEY).toBe("sk-live-abc");
    expect(values.WEBHOOK_SECRET).toBe("whsec-xyz");
    expect(Object.keys(values).some((key) => key.startsWith("OCEL_VAR_"))).toBe(false);
  });

  it("lets a sealed value win over the same name in the bundle's env", async () => {
    const { edge } = variablesInvoker({
      bundleEnv: { __NEXT_BUILD_ID: "t", STRIPE_API_KEY: "from-bundle" },
      variables: { envelope: GO_ENVELOPE },
      sealed: bytesOf(GO_SEALED),
    });

    expect((await workerEnv(edge)).STRIPE_API_KEY).toBe("sk-live-abc");
  });

  it("unseals a nonce-prefixed AES-GCM payload of its own making", async () => {
    const values = { TOKEN: "t0ken" };
    const sealed = await seal(GO_ENVELOPE, JSON.stringify(values));
    expect(sealed.length).toBe(NONCE_BYTES + JSON.stringify(values).length + TAG_BYTES);

    const { edge } = variablesInvoker({ variables: { envelope: GO_ENVELOPE }, sealed });

    expect((await workerEnv(edge)).TOKEN).toBe("t0ken");
  });

  const WRAPPING_KEY = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=";

  async function wrap(envelopeKey: string, envelope: string): Promise<string> {
    const key = await crypto.subtle.importKey(
      "raw",
      bytesOf(envelopeKey),
      { name: "AES-GCM" },
      false,
      ["encrypt"],
    );
    const nonce = crypto.getRandomValues(new Uint8Array(NONCE_BYTES));
    const wrapped = new Uint8Array(
      await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce }, key, bytesOf(envelope)),
    );
    const framed = new Uint8Array(nonce.length + wrapped.length);
    framed.set(nonce);
    framed.set(wrapped, nonce.length);
    return btoa(String.fromCharCode(...framed));
  }

  it("unwraps the envelope with the worker's own key before unsealing", async () => {
    const sealed = bytesOf(GO_SEALED);
    const { edge } = variablesInvoker({
      variables: { envelope: await wrap(WRAPPING_KEY, GO_ENVELOPE), envelopeKey: WRAPPING_KEY },
      sealed,
    });

    const values = await workerEnv(edge);
    expect(values.STRIPE_API_KEY).toBe("sk-live-abc");
    expect(values.WEBHOOK_SECRET).toBe("whsec-xyz");
  });

  it("unwraps what the origin's stack wrapped in Go", async () => {
    const GO_WRAPPING_KEY = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8=";
    const GO_WRAPPED =
      "UE2Q24dOC8dGzwjPRGx8m2WTic1ZsjF5ExTE/LLYg+aA7BMnZbTrTCQswobxFS0ot4qk4AKLEnMQZFfS";
    const { edge } = variablesInvoker({
      variables: { envelope: GO_WRAPPED, envelopeKey: GO_WRAPPING_KEY },
      sealed: bytesOf(GO_SEALED),
    });

    expect((await workerEnv(edge)).STRIPE_API_KEY).toBe("sk-live-abc");
  });

  it("refuses an envelope wrapped for another worker", async () => {
    const other = "//////////////////////////////////////////8=";
    const { edge } = variablesInvoker({
      variables: { envelope: await wrap(other, GO_ENVELOPE), envelopeKey: WRAPPING_KEY },
      sealed: bytesOf(GO_SEALED),
    });

    await expect(edge("e", new Request("https://x/"))).rejects.toThrow(
      "ocel: the envelope was not wrapped for this worker",
    );
  });

  it("refuses a bare envelope once it has a key, rather than trusting the record", async () => {
    const { edge } = variablesInvoker({
      variables: { envelope: GO_ENVELOPE, envelopeKey: WRAPPING_KEY },
      sealed: bytesOf(GO_SEALED),
    });

    await expect(edge("e", new Request("https://x/"))).rejects.toThrow(
      "ocel: the envelope was not wrapped for this worker",
    );
  });

  it("names a non-JSON payload without quoting what it decrypted", async () => {
    const plaintext = "sk-live-abc is not JSON";
    const sealed = await seal(GO_ENVELOPE, plaintext);

    const { edge } = variablesInvoker({ variables: { envelope: GO_ENVELOPE }, sealed });

    const failure = await edge("e", new Request("https://x/")).then(
      () => null,
      (error: Error) => error,
    );
    expect(failure?.message).toBe("ocel: sealed edge variables are not JSON");
  });

  it("refuses to load when the envelope names bytes the store has not got", async () => {
    const { edge } = variablesInvoker({ variables: { envelope: GO_ENVELOPE } });

    await expect(edge("e", new Request("https://x/"))).rejects.toThrow(
      /no sealed edge variables at .*\/sealed\.bin/,
    );
  });

  it("never reads the sealed object when nothing was sealed", async () => {
    const { edge, reads } = variablesInvoker({ variables: { env: { A: "one" } } });

    await workerEnv(edge);
    expect(reads.filter((key) => key.endsWith("/sealed.bin"))).toEqual([]);
  });

  it("reloads the isolate when only the release fingerprint changed", async () => {
    const release = (releaseFingerprint: string) =>
      variablesInvoker({
        handler: COUNTER,
        id: "shared-bundle-variables",
        variables: { releaseFingerprint },
      }).edge;
    const served = async (edge: EdgeInvoker) => (await edge("e", new Request("https://x/"))).text();

    expect(await served(release("fp-1"))).toBe("1");
    expect(await served(release("fp-1"))).toBe("2");
    expect(await served(release("fp-2"))).toBe("1");
  });
});
