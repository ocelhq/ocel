import type { Route } from "@next/routing";
import { tagSnapshotKey } from "@framework/next-cache";
import { describe, expect, it } from "vitest";

import { dispatchResult, ruleDestinationPathname, serve, type RouteDeps } from "../src/index";
import { refreshBackoffSeconds, sentinelUrl } from "../src/cache";
import { coloDeps } from "./cache-deps";
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

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

function baseDeps(overrides: Partial<RouteDeps> = {}): RouteDeps {
  return {
    manifest: {
      buildId: "test",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {},
    },
    functionUrls: {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: noAssets(),
    ...overrides,
  };
}

describe("dispatchResult", () => {
  it("serves a static route from the R2 asset store", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/next.svg": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/next.svg": "<svg/>" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/next.svg" },
      new Request("https://app.example/next.svg"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
  });

  it("varies a statically-dispatched page on the flight headers when the request carries one", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/dashboard": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/dashboard": "<html/>" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/dashboard" },
      new Request("https://app.example/dashboard", { headers: { rsc: "1" } }),
      deps,
    );

    expect(res.headers.get("vary")).toBe(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch, next-url",
    );
  });

  it("does not vary a statically-dispatched page when the request carries no flight header", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/dashboard": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/dashboard": "<html/>" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/dashboard" },
      new Request("https://app.example/dashboard"),
      deps,
    );

    expect(res.headers.has("vary")).toBe(false);
  });

  it("does not vary a /_next/static/* asset even when the request carries a flight header", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({
        "/_next/static/chunks/app.js": "console.log(1)",
      }),
    });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/_next/static/chunks/app.js", {
        headers: { rsc: "1" },
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(res.headers.has("vary")).toBe(false);
  });

  it("serves a statically-optimized dynamic page under its manifest key", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "/docs",
        pathnames: ["/docs/[slug]"],
        routes: {},
        dispatch: { "/docs/[slug]": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/docs/[slug].html": "<html>slug</html>" }),
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/docs/[slug]",
        invocationTarget: { pathname: "/docs/slug-1" },
      },
      new Request("https://app.example/docs/slug-1"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>slug</html>");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(res.headers.get("x-matched-path")).toBe("/docs/[slug]");
  });

  it("serves an ordinary static page under a basePath", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "/docs",
        pathnames: ["/docs/hello"],
        routes: {},
        dispatch: { "/docs/hello": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/docs/hello.html": "<html>hello</html>" }),
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/docs/hello",
        invocationTarget: { pathname: "/docs/hello" },
      },
      new Request("https://app.example/docs/hello"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>hello</html>");
  });

  describe("the methods a prerendered document answers", () => {
    function documentDeps(onOrigin: (req: Request) => void = () => {}) {
      return baseDeps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/about", "/blog"],
          routes: {},
          dispatch: {
            "/about": { kind: "static" },
            "/blog": { kind: "prerender", id: "/blog", config: {} },
          },
        },
        functionUrls: { "/blog": "https://fn.example.com" },
        fetch: (async (req: Request) => {
          onOrigin(req);
          return new Response("rendered", { status: 200 });
        }) as unknown as typeof fetch,
        assetStore: assetStoreServing({ "/about.html": "<html>about</html>" }),
      });
    }

    function dispatchTo(pathname: string, request: Request, deps = documentDeps()) {
      return dispatchResult(
        { resolvedPathname: pathname, invocationTarget: { pathname } },
        request,
        deps,
      );
    }

    it.each(["/about", "/blog"])("answers a POST to %s with 405", async (pathname) => {
      let origins = 0;
      const deps = documentDeps(() => (origins += 1));

      const res = await dispatchTo(
        pathname,
        new Request(`https://app.example${pathname}`, { method: "POST" }),
        deps,
      );

      expect(res.status).toBe(405);
      expect(res.headers.get("allow")).toBe("GET, HEAD");
      expect(origins).toBe(0);
    });

    it.each(["/about", "/blog"])("still answers a HEAD to %s", async (pathname) => {
      const res = await dispatchTo(
        pathname,
        new Request(`https://app.example${pathname}`, { method: "HEAD" }),
      );

      expect(res.status).toBe(200);
    });

    it("lets a server action through to the origin", async () => {
      let captured: Request | undefined;
      const deps = documentDeps((req) => (captured = req));

      const res = await dispatchTo(
        "/blog",
        new Request("https://app.example/blog", {
          method: "POST",
          headers: { "next-action": "abc" },
          body: "[]",
        }),
        deps,
      );

      expect(res.status).toBe(200);
      expect(captured?.method).toBe("POST");
    });

    it.each(["application/x-www-form-urlencoded", "multipart/form-data; boundary=x"])(
      "lets a %s form post through to the origin",
      async (contentType) => {
        let captured: Request | undefined;
        const deps = documentDeps((req) => (captured = req));

        const res = await dispatchTo(
          "/blog",
          new Request("https://app.example/blog", {
            method: "POST",
            headers: { "content-type": contentType },
            body: "a=1",
          }),
          deps,
        );

        expect(res.status).toBe(200);
        expect(captured?.method).toBe("POST");
      },
    );
  });

  it.each([200, 307, 404, 405, 500])(
    "restores the empty body a %i sentinel response stands for",
    async (status) => {
      const deps = baseDeps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: [],
          routes: {},
          dispatch: { "/status": { kind: "lambda", id: "/status" } },
        },
        functionUrls: { "/status": "https://fn.example.com" },
        fetch: (async () =>
          new Response("\n", {
            status,
            headers: { "x-ocel-empty-body": "1", "x-custom": "kept" },
          })) as unknown as typeof fetch,
      });

      const res = await dispatchResult(
        { resolvedPathname: "/status", invocationTarget: { pathname: "/status" } },
        new Request("https://app.example/status"),
        deps,
      );

      expect(res.status).toBe(status);
      expect(await res.text()).toBe("");
      expect(res.headers.get("x-ocel-empty-body")).toBeNull();
      expect(res.headers.get("x-custom")).toBe("kept");
    },
  );

  it("strips x-next-cache-tags from a Lambda-forwarded response", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/tags": { kind: "lambda", id: "/tags" } },
      },
      functionUrls: { "/tags": "https://fn.example.com" },
      fetch: (async () =>
        new Response("from-lambda", {
          status: 200,
          headers: { "x-next-cache-tags": "tag1,tag2", "x-custom": "kept" },
        })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/tags", invocationTarget: { pathname: "/tags" } },
      new Request("https://app.example/tags"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-lambda");
    expect(res.headers.get("x-next-cache-tags")).toBeNull();
    expect(res.headers.get("x-custom")).toBe("kept");
  });

  it("leaves a body that is genuinely one byte alone", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/tiny": { kind: "lambda", id: "/tiny" } },
      },
      functionUrls: { "/tiny": "https://fn.example.com" },
      fetch: (async () => new Response("\n", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/tiny", invocationTarget: { pathname: "/tiny" } },
      new Request("https://app.example/tiny"),
      deps,
    );

    expect(await res.text()).toBe("\n");
  });

  it("forwards a lambda route to its Function URL, preserving path and query", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
      },
      functionUrls: { "/api/documents": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("from-lambda", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://app.example/api/documents?q=1"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-lambda");
    expect(captured?.url).toBe("https://fn.example.com/api/documents?q=1");
  });

  it("percent-encodes the request-target characters a Function URL rejects", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/dynamic/[first]": { kind: "lambda", id: "fn" } },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("from-lambda", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/dynamic/[first]",
        invocationTarget: { pathname: "/dynamic/[first]" },
      },
      new Request("https://app.example/dynamic/%5Bfirst%5D"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(captured?.url).toBe("https://fn.example.com/dynamic/%5Bfirst%5D");
  });

  it("leaves an already-encoded request target encoded exactly once", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/caret": { kind: "lambda", id: "fn" } },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("from-lambda", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/caret",
        invocationTarget: { pathname: "/caret/a%5Eb^c|d" },
      },
      new Request("https://app.example/caret"),
      deps,
    );

    expect(new URL(captured!.url).pathname).toBe("/caret/a%5Eb%5Ec%7Cd");
  });

  it("sets x-forwarded-host to the public host so Next's Server Action origin check passes", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
      },
      functionUrls: { "/api/documents": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://cachelab.ocel.dev/api/documents", {
        method: "POST",
        headers: { origin: "https://cachelab.ocel.dev" },
      }),
      deps,
    );

    expect(captured?.headers.get("x-forwarded-host")).toBe("cachelab.ocel.dev");
    expect(captured?.headers.get("x-forwarded-proto")).toBe("https");
  });

  it("forwards a POST body intact after buffering it off the request stream", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
      },
      functionUrls: { "/api/documents": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const payload = "name=cachelab&value=1";
    await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://cachelab.ocel.dev/api/documents", {
        method: "POST",
        body: payload,
      }),
      deps,
    );

    expect(await captured?.text()).toBe(payload);
  });

  it("forwards a lambda route through originFetch (signed), not plain fetch", async () => {
    let signedUrl: string | undefined;
    let plainCalled = false;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/documents": { kind: "lambda", id: "/api/documents" } },
      },
      functionUrls: { "/api/documents": "https://fn.example.com" },
      fetch: (async () => {
        plainCalled = true;
        return new Response("plain", { status: 200 });
      }) as unknown as typeof fetch,
      originFetch: (async (req: Request) => {
        signedUrl = req.url;
        return new Response("signed", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/api/documents",
        invocationTarget: { pathname: "/api/documents" },
      },
      new Request("https://app.example/api/documents"),
      deps,
    );

    expect(await res.text()).toBe("signed");
    expect(signedUrl).toBe("https://fn.example.com/api/documents");
    expect(plainCalled).toBe(false);
  });

  it("forwards an external rewrite through plain fetch, never originFetch", async () => {
    let plainUrl: string | undefined;
    let signedCalled = false;
    const deps = baseDeps({
      fetch: (async (req: Request) => {
        plainUrl = req.url;
        return new Response("external", { status: 200 });
      }) as unknown as typeof fetch,
      originFetch: (async () => {
        signedCalled = true;
        return new Response("signed", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { externalRewrite: "https://other.example/proxied" },
      new Request("https://app.example/x"),
      deps,
    );

    expect(await res.text()).toBe("external");
    expect(plainUrl).toBe("https://other.example/proxied");
    expect(signedCalled).toBe(false);
  });

  it("invokes the parent function for a prerender route until ISR lands", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/": { kind: "prerender", id: "/" } },
      },
      functionUrls: { "/": "https://fn.example.com" },
      fetch: (async () => new Response("rendered", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("rendered");
  });

  function missingCache(): NonNullable<RouteDeps["cache"]> {
    return coloDeps({
      cache: {
        match: async () => undefined,
        put: async () => {},
      } as unknown as Cache,
      waitUntil: () => {},
    });
  }

  function bypassDeps(bypassKey: string): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/preview": {
            kind: "prerender",
            id: "/preview",
            config: { bypassFor: [{ type: "cookie", key: bypassKey }] },
          },
        },
      },
      functionUrls: { "/preview": "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: missingCache(),
    });
  }

  async function dispatchPreview(deps: RouteDeps, cookie: string) {
    return dispatchResult(
      { resolvedPathname: "/preview", invocationTarget: { pathname: "/preview" } },
      new Request("https://app.example/preview", { headers: { cookie } }),
      deps,
    );
  }

  it("does not treat a valueless cookie as a bypass match on a key prefix", async () => {
    const res = await dispatchPreview(bypassDeps("badcooki"), "badcookie");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("bypasses the cache when a real bypass cookie is present", async () => {
    const res = await dispatchPreview(bypassDeps("preview"), "preview=1");
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  function draftDeps(capture: (req: Request) => void): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/ssg-draft-mode/test-1": {
            kind: "prerender",
            id: "/ssg-draft-mode/[[...route]]",
            config: {
              allowHeader: ["host", "x-matched-path", "x-prerender-revalidate"],
              bypassFor: [{ type: "header", key: "next-action" }],
            },
          },
        },
      },
      functionUrls: { "/ssg-draft-mode/[[...route]]": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        capture(req);
        return new Response("rendered", { status: 200 });
      }) as unknown as typeof fetch,
      cache: missingCache(),
    });
  }

  function draftRequest(init: RequestInit = {}) {
    return new Request("https://app.example/ssg-draft-mode/test-1", {
      headers: { cookie: "__prerender_bypass=abc123" },
      ...init,
    });
  }

  async function dispatchDraft(deps: RouteDeps, request: Request) {
    return dispatchResult(
      {
        resolvedPathname: "/ssg-draft-mode/test-1",
        invocationTarget: { pathname: "/ssg-draft-mode/test-1" },
      },
      request,
      deps,
    );
  }

  it("forwards the draft cookie to a prerender origin, which allowHeader omits", async () => {
    let captured: Request | undefined;
    const res = await dispatchDraft(
      draftDeps((req) => (captured = req)),
      draftRequest(),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(captured?.headers.get("cookie")).toBe("__prerender_bypass=abc123");
  });

  it("forwards a server action to a prerender origin with its own headers", async () => {
    let captured: Request | undefined;
    const res = await dispatchDraft(
      draftDeps((req) => (captured = req)),
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { cookie: "session=xyz", "next-action": "abc" },
        body: "{}",
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(captured?.headers.get("cookie")).toBe("session=xyz");
  });

  it("drops a client-forged next-resume from a server-action prerender forward", async () => {
    let captured: Request | undefined;
    await dispatchDraft(
      draftDeps((req) => (captured = req)),
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { "next-resume": "1", "next-action": "abc" },
        body: "[1,{}]",
      }),
    );

    expect(captured?.headers.get("next-resume")).toBeNull();
  });

  it("forwards the RSC-family headers to a prerender origin past allowHeader", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: { allowHeader: ["host"] },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: missingCache(),
    });

    await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog?_rsc=abc", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "1",
          "next-router-state-tree": "%5B%22%22%5D",
        },
      }),
      deps,
    );

    expect(captured?.headers.get("rsc")).toBe("1");
    expect(captured?.headers.get("next-router-prefetch")).toBe("1");
    expect(captured?.headers.get("next-router-state-tree")).toBe("%5B%22%22%5D");
  });

  it("forwards the client's cookie to an uncacheable prerender origin", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: { allowHeader: ["host"] },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: missingCache(),
      interception: {
        config: { isrPrefix: "prod/p/app/build" },
        now: () => 2_000,
        store: { async get() { return null; } },
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "2",
          cookie: "testCookie=initialValue",
        },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(captured?.headers.get("cookie")).toBe("testCookie=initialValue");
    expect(captured?.headers.get("purpose")).toBe("prefetch");
  });

  it("still strips the cookie from a cacheable prerender origin", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: { allowHeader: ["host"] },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: missingCache(),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "1",
          cookie: "testCookie=initialValue",
        },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(captured?.headers.get("cookie")).toBeNull();
  });

  const interceptionConfig = { isrPrefix: "prod/p/app/build" };

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

  const entryKey = (routePath: string) =>
    `${interceptionConfig.isrPrefix}/cache/${routePath}.cache.json`;

  function interceptDeps(
    lambdaBody: string,
    storeEntry: unknown | null,
    lambdaHeaders: Record<string, string> = {},
  ): { deps: RouteDeps; lambdaCalls: () => number } {
    let lambda = 0;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async () => {
        lambda++;
        return new Response(lambdaBody, {
          status: 200,
          headers: { "cache-control": "s-maxage=60", ...lambdaHeaders },
        });
      }) as unknown as typeof fetch,
      cache: missingCache(),
      interception: {
        config: interceptionConfig,
        now: () => 2_000,
        store: storeOf(storeEntry ? { [entryKey("blog")]: storeEntry } : {}),
      },
    });
    return { deps, lambdaCalls: () => lambda };
  }

  const dispatchBlog = (
    deps: RouteDeps,
    request = new Request("https://app.example/blog"),
  ) =>
    dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      request,
      deps,
    );

  it("serves a prerender from interception without invoking the Lambda", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", {
      lastModified: 1_000,
      value: { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} },
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(res.headers.get("x-nextjs-cache")).toBe("HIT");
    expect(await res.text()).toBe("<html>edge</html>");
    expect(lambdaCalls()).toBe(0);
  });

  it("falls open to the Lambda when interception misses in the store", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", null);

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await res.text()).toBe("from-lambda");
    expect(lambdaCalls()).toBe(1);
  });

  it("leaves the Lambda's own x-nextjs-cache alone on a store miss", async () => {
    const { deps } = interceptDeps("from-lambda", null, {
      "x-nextjs-cache": "REVALIDATED",
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-nextjs-cache")).toBe("REVALIDATED");
  });

  it("serves a stale complete entry from the store and refreshes via the Lambda behind the request", async () => {
    let lambda = 0;
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async () => {
        lambda++;
        return new Response("regenerated", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf({
          [entryKey("blog")]: {
            lastModified: 1_000,
            value: { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} },
          },
        }),
      },
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>edge</html>");

    await Promise.all(pending);
    expect(lambda).toBe(1);
  });

  it.each([
    ["a complete entry", "/blog", { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} }],
    ["a PPR shell", "/ppr", { kind: "APP_PAGE", html: "[shell]", postponed: "POSTPONED", status: 200, headers: {} }],
  ])("caps the R2 tier's admission wait on %s's remaining stale window", async (_name, path, value) => {
    const bounds: number[] = [];
    const pending: Promise<unknown>[] = [];
    const id = path.slice(1);
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          [path]: {
            kind: "prerender",
            id: path,
            config: "postponed" in value ? { renderingMode: "PARTIALLY_STATIC" } : {},
            fallback: { initialRevalidate: 60, initialExpiration: 3600 },
          },
        },
      },
      functionUrls: { [path]: "https://fn.example.com" },
      fetch: (async () =>
        new Response("regenerated", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
        admissionDelay: (staleForMs: number) => {
          bounds.push(staleForMs);
          return Promise.resolve();
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 3_599_500,
        store: storeOf({ [entryKey(id)]: { lastModified: 1_000, value } }),
      },
    });

    await dispatchResult(
      { resolvedPathname: path, invocationTarget: { pathname: path } },
      new Request(`https://app.example${path}`),
      deps,
    );
    await Promise.all(pending);

    expect(bounds).toEqual([500]);
  });

  function refreshOverStore(
    entries: Record<string, unknown>,
    landsDuringWait?: Record<string, unknown>,
    lambdaStatus = 200,
    landsModified = 1_000 + 61_000,
  ) {
    let lambda = 0;
    const pending: Promise<unknown>[] = [];
    const puts: {
      url: string;
      body: string;
      cacheControl: string | null;
      entryModified: string | null;
    }[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async () => {
        lambda++;
        return new Response("regenerated", {
          status: lambdaStatus,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async (request: Request, response: Response) => {
            puts.push({
              url: new Request(request).url,
              cacheControl: response.headers.get("cache-control"),
              entryModified: response.headers.get("x-ocel-entry-modified"),
              body: await response.text(),
            });
          },
          delete: async () => false,
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
        admissionDelay: async () => {
          if (landsDuringWait === undefined) return;
          entries[entryKey("blog")] = {
            lastModified: landsModified,
            value: landsDuringWait,
          };
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf(entries),
      },
    });
    return { deps, pending, puts, lambdaCalls: () => lambda };
  }

  const pageValue = (html: string, extra: Record<string, unknown> = {}) => ({
    kind: "APP_PAGE",
    html,
    status: 200,
    headers: {},
    ...extra,
  });

  const staleBelow = () => ({
    [entryKey("blog")]: { lastModified: 1_000, value: pageValue("<html>edge</html>") },
  });

  const stalePrefetchableBelow = () => ({
    [entryKey("blog")]: {
      lastModified: 1_000,
      value: pageValue("[shell]", {
        postponed: "POSTPONED",
        rscData: btoa("[shell-rsc]"),
      }),
    },
  });

  const prefetchRequest = () =>
    new Request("https://app.example/blog", {
      headers: { RSC: "1", "next-router-prefetch": "1" },
    });

  it("does not render when R2 already holds a fresher entry by the time the refresh is admitted", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>fresher</html>"),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
  });

  it("refills the colo entry from R2 rather than from a render it skipped", async () => {
    const { deps, pending, puts } = refreshOverStore(
      staleBelow(),
      pageValue("<html>fresher</html>"),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(puts.map((put) => put.body)).toContain("<html>fresher</html>");
  });

  it("backs the R2 tier's refresh off when the Lambda refuses it", async () => {
    const { deps, pending, puts } = refreshOverStore(staleBelow(), undefined, 429);

    await dispatchBlog(deps);
    await Promise.all(pending);

    const sentinelWrites = puts.filter(
      (put) => put.url === sentinelUrl("p1/web/d1:/blog"),
    );
    expect(sentinelWrites.at(-1)?.cacheControl).toBe(
      `max-age=${refreshBackoffSeconds}`,
    );
  });

  it("renders when R2 is still stale by the time the refresh is admitted", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(staleBelow());

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  it("promotes a stale entry below that is newer than the one being refreshed", async () => {
    const { deps, pending, puts, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>newer</html>"),
      200,
      1_500,
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
    expect(
      puts.find((put) => put.body === "<html>newer</html>")?.entryModified,
    ).toBe(String(1_500));
  });

  it("renders when the stale entry below is older than the one being refreshed", async () => {
    const { deps, pending, puts, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("<html>older</html>"),
      200,
      500,
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
    expect(puts.map((put) => put.body)).not.toContain("<html>older</html>");
  });

  it("regenerates the stale entry a prefetch was served from", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(stalePrefetchableBelow());

    await dispatchBlog(deps, prefetchRequest());
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  it("still skips the render when a prefetch's entry was refreshed below during the wait", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      stalePrefetchableBelow(),
      pageValue("[fresher shell]", {
        postponed: "POSTPONED",
        rscData: btoa("[fresher-shell-rsc]"),
      }),
    );

    await dispatchBlog(deps, prefetchRequest());
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(0);
  });

  it("dates a mirrored prefetch by the entry's own modified time, not by the mirror", async () => {
    const { deps, pending, puts } = refreshOverStore(
      stalePrefetchableBelow(),
      pageValue("[fresher shell]", {
        postponed: "POSTPONED",
        rscData: btoa("[fresher-shell-rsc]"),
      }),
    );

    await dispatchBlog(deps, prefetchRequest());
    await Promise.all(pending);

    const mirrored = puts.filter((put) => put.url.endsWith(".prefetch.rsc"));
    expect(mirrored.map((put) => put.entryModified).sort()).toEqual([
      String(1_000),
      String(1_000 + 61_000),
    ]);
  });

  it("renders when the fresher entry below cannot refill the colo's variant", async () => {
    const { deps, pending, lambdaCalls } = refreshOverStore(
      staleBelow(),
      pageValue("[shell]", { postponed: "POSTPONED" }),
    );

    await dispatchBlog(deps);
    await Promise.all(pending);

    expect(lambdaCalls()).toBe(1);
  });

  it("skips the render when a variant with no colo entry is fresh below", async () => {
    const pprEntry = (html: string) => ({
      lastModified: html === "[shell]" ? 1_000 : 1_000 + 61_000,
      value: pageValue(html, {
        postponed: "POSTPONED",
        rscData: btoa(`${html}-rsc`),
      }),
    });
    const entries: Record<string, unknown> = { [entryKey("ppr")]: pprEntry("[shell]") };
    let revalidations = 0;
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/ppr": {
            kind: "prerender",
            id: "/ppr",
            config: { renderingMode: "PARTIALLY_STATIC" },
            fallback: { initialRevalidate: 60 },
            pprChain: { headers: {} },
          },
        },
      },
      functionUrls: { "/ppr": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        if (req.headers.has("x-prerender-revalidate")) revalidations++;
        return new Response("[dynamic]", { status: 200 });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
        admissionDelay: async () => {
          entries[entryKey("ppr")] = pprEntry("[fresher shell]");
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf(entries),
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/ppr", invocationTarget: { pathname: "/ppr" } },
      new Request("https://app.example/ppr", { headers: { RSC: "1" } }),
      deps,
    );
    await res.text();
    await Promise.all(pending);

    expect(revalidations).toBe(0);
  });

  function coloHoldingSentinel(refreshKey: string): Cache {
    const url = sentinelUrl(refreshKey);
    return {
      match: async (request: Request) =>
        request.url === url ? new Response(null) : undefined,
      put: async () => {},
      delete: async () => false,
    } as unknown as Cache;
  }

  it("leaves the stale R2 refresh to whichever isolate holds the colo's sentinel", async () => {
    let lambda = 0;
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async () => {
        lambda++;
        return new Response("regenerated", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: coloHoldingSentinel("p1/web/d1:/blog"),
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf({
          [entryKey("blog")]: {
            lastModified: 1_000,
            value: { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} },
          },
        }),
      },
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(res.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await res.text()).toBe("<html>edge</html>");

    await Promise.all(pending);
    expect(lambda).toBe(0);
  });

  it("leaves a stale PPR shell's refresh to whichever isolate holds the colo's sentinel", async () => {
    const origins: Request[] = [];
    const pending: Promise<unknown>[] = [];
    const pprDispatch = {
      "/ppr": {
        kind: "prerender",
        id: "/ppr",
        config: {},
        fallback: { initialRevalidate: 60, initialExpiration: 3600 },
        pprChain: { headers: { "next-resume": "1" } },
      },
    };
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: pprDispatch,
      },
      functionUrls: { "/ppr": "https://fn.example.com" },
      fetch: (async (req: Request) => {
        origins.push(req);
        return new Response("[dynamic]", { status: 200 });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: coloHoldingSentinel("p1/web/d1:/ppr"),
        waitUntil: (p: Promise<unknown>) => {
          pending.push(p);
        },
      }),
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf({
          [entryKey("ppr")]: {
            lastModified: 1_000,
            value: {
              kind: "APP_PAGE",
              html: "[shell]",
              postponed: "POSTPONED",
              status: 200,
              headers: {},
            },
          },
        }),
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/ppr", invocationTarget: { pathname: "/ppr" } },
      new Request("https://app.example/ppr"),
      deps,
    );

    expect(await res.text()).toBe("[shell][dynamic]");
    await Promise.all(pending);
    expect(origins.map((req) => req.method)).toEqual(["POST"]);
  });

  it("refreshes the colo entry with the Lambda's fresh body after a stale R2 hit", async () => {
    const pending: Promise<unknown>[] = [];
    const stored = new Map<string, Response>();
    let resolveLambda!: (response: Response) => void;
    const lambdaResponse = new Promise<Response>((resolve) => {
      resolveLambda = resolve;
    });
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (() => lambdaResponse) as unknown as typeof fetch,
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
      interception: {
        config: interceptionConfig,
        now: () => 1_000 + 61_000,
        store: storeOf({
          [entryKey("blog")]: {
            lastModified: 1_000,
            value: { kind: "APP_PAGE", html: "<html>edge</html>", status: 200, headers: {} },
          },
        }),
      },
    });

    const res = await dispatchBlog(deps);

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("<html>edge</html>");

    resolveLambda(
      new Response("fresh-lambda-body", {
        status: 200,
        headers: { "cache-control": "s-maxage=60" },
      }),
    );
    await Promise.all(pending);

    const follow = await dispatchBlog(deps);
    expect(follow.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await follow.text()).toBe("fresh-lambda-body");
  });

  it("sends a runtime prefetch (next-router-prefetch: 2) to the Lambda, uncached", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", {
      lastModified: 1_000,
      value: {
        kind: "APP_PAGE",
        html: "<html>edge</html>",
        rscData: btoa("RSC"),
        status: 200,
        headers: {},
        postponed: "PP",
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/blog", {
        headers: { RSC: "1", "next-router-prefetch": "2" },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await res.text()).toBe("from-lambda");
    expect(lambdaCalls()).toBe(1);
  });

  it("skips interception for a pages-router _next/data request (serves JSON via Lambda)", async () => {
    const { deps, lambdaCalls } = interceptDeps("from-lambda", {
      lastModified: 1_000,
      value: { kind: "PAGES", html: "<html>edge</html>", status: 200, headers: {} },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
      new Request("https://app.example/_next/data/t/blog.json"),
      deps,
    );

    expect(await res.text()).toBe("from-lambda");
    expect(lambdaCalls()).toBe(1);
  });

  function pprDeps(opts: {
    resume: string;
    resumeHeaders?: Record<string, string>;
    entryPath?: string;
    entry: Record<string, unknown> | null;
    dispatch?: Record<string, unknown>;
    signed?: boolean;
  }): {
    deps: RouteDeps;
    resumeRequests: () => Request[];
    cachePuts: () => number;
    plainCalled: () => boolean;
    storeReads: () => number;
  } {
    const resumeRequests: Request[] = [];
    let puts = 0;
    let plainCalled = false;
    let reads = 0;
    const countingStore = (store: { get: (key: string) => Promise<unknown> }) => ({
      get: async (key: string) => {
        reads++;
        return store.get(key);
      },
    });
    const record = (async (req: Request) => {
      resumeRequests.push(req);
      return new Response(opts.resume, { status: 200, headers: opts.resumeHeaders });
    }) as unknown as typeof fetch;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: opts.dispatch ?? {
          "/ppr": {
            kind: "prerender",
            id: "/ppr",
            config: {},
            fallback: { initialRevalidate: 60, initialExpiration: 3600 },
            pprChain: { headers: { "next-resume": "1" } },
          },
        },
      },
      functionUrls: { "/ppr": "https://fn.example.com", "/posts/[id]": "https://fn.example.com" },
      fetch: opts.signed
        ? ((async (req: Request) => {
            plainCalled = true;
            return record(req);
          }) as unknown as typeof fetch)
        : record,
      originFetch: opts.signed ? record : undefined,
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {
            puts++;
          },
        } as unknown as Cache,
        waitUntil: () => {},
      }),
      interception: {
        config: interceptionConfig,
        now: () => 2_000,
        store: countingStore(
          storeOf(
            opts.entry ? { [entryKey(opts.entryPath ?? "ppr")]: opts.entry } : {},
          ),
        ),
      },
    });
    return {
      deps,
      resumeRequests: () => resumeRequests,
      cachePuts: () => puts,
      plainCalled: () => plainCalled,
      storeReads: () => reads,
    };
  }

  const pprShellEntry = {
    lastModified: 1_000,
    value: {
      kind: "APP_PAGE",
      html: "[shell]",
      postponed: "POSTPONED",
      status: 200,
      headers: {},
    },
  };

  const dispatchPpr = (deps: RouteDeps, headers?: Record<string, string>) =>
    dispatchResult(
      { resolvedPathname: "/ppr", invocationTarget: { pathname: "/ppr" } },
      new Request("https://app.example/ppr", { headers }),
      deps,
    );

  it("strips x-next-cache-tags from a prerender's origin render", async () => {
    const { deps } = pprDeps({
      resume: "[rendered]",
      resumeHeaders: { "x-next-cache-tags": "tag1,tag2", "x-custom": "kept" },
      entry: null,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[rendered]");
    expect(res.headers.get("x-next-cache-tags")).toBeNull();
    expect(res.headers.get("x-custom")).toBe("kept");
  });

  it("composes shell + resumed dynamic for a PPR entry and POSTs the resume", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellEntry,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    const [resume] = resumeRequests();
    expect(resume.method).toBe("POST");
    expect(resume.headers.get("next-resume")).toBe("1");
    expect(await resume.text()).toBe("POSTPONED");
  });

  it("stamps next-resume on the real resume even when the client sent one", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellEntry,
    });

    const res = await dispatchPpr(deps, { "next-resume": "1" });

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()[0]?.headers.get("next-resume")).toBe("1");
  });

  it("POSTs the resume through the signed origin seam, never plain fetch", async () => {
    const { deps, resumeRequests, plainCalled } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellEntry,
      signed: true,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()).toHaveLength(1);
    expect(resumeRequests()[0].method).toBe("POST");
    expect(plainCalled()).toBe(false);
  });

  it("serves a PPR prefetch as the static shell, never a resume", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: {
        lastModified: 1_000,
        value: {
          kind: "APP_PAGE",
          html: "[shell]",
          rscData: btoa("[rsc-shell]"),
          postponed: "POSTPONED",
          status: 200,
          headers: {},
        },
      },
    });

    const res = await dispatchPpr(deps, { rsc: "1", "next-router-prefetch": "1" });

    expect(resumeRequests()).toHaveLength(0);
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("[rsc-shell]");
  });

  const pprShellWithFlight = {
    lastModified: 1_000,
    value: {
      kind: "APP_PAGE",
      html: "[shell]",
      rscData: btoa("[rsc-shell]"),
      postponed: "POSTPONED",
      status: 200,
      headers: {},
    },
  };

  it("answers a Flight navigation with the origin's render alone, never composed onto the shell", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    const res = await dispatchPpr(deps, { rsc: "1" });

    expect(await res.text()).toBe("[dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(resumeRequests()).toHaveLength(1);
    expect(resumeRequests()[0].method).toBe("GET");
  });

  it("does not read the shell out of the store for a Flight navigation", async () => {
    const { deps, storeReads } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    await (await dispatchPpr(deps, { rsc: "1" })).text();

    expect(storeReads()).toBe(0);
  });

  it("still composes the document request against the same entry", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    const res = await dispatchPpr(deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(resumeRequests()[0].method).toBe("POST");
  });

  it("treats a malformed RSC header as a document request, matching the origin", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entry: pprShellWithFlight,
    });

    const res = await dispatchPpr(deps, { rsc: "2" });

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()[0].method).toBe("POST");
  });

  it("never puts a composed PPR response into the colo cache", async () => {
    const { deps, cachePuts } = pprDeps({ resume: "[dynamic]", entry: pprShellEntry });

    const res = await dispatchPpr(deps);
    await res.text();

    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(cachePuts()).toBe(0);
  });

  it("forwards the client's cookie to the resume origin", async () => {
    const { deps, resumeRequests } = pprDeps({ resume: "[dynamic]", entry: pprShellEntry });

    await dispatchPpr(deps, { cookie: "session=abc" });

    expect(resumeRequests()[0].headers.get("cookie")).toBe("session=abc");
  });

  it("bypasses PPR entirely when the draft cookie is present", async () => {
    const { deps, resumeRequests } = pprDeps({ resume: "from-lambda", entry: pprShellEntry });

    const res = await dispatchPpr(deps, { cookie: "__prerender_bypass=1" });

    expect(resumeRequests()[0].method).toBe("GET");
    expect(await res.text()).toBe("from-lambda");
  });

  it("resumes a concrete dynamic path from the [id] fallback shell", async () => {
    const { deps, resumeRequests } = pprDeps({
      resume: "[dynamic]",
      entryPath: "posts/[id]",
      entry: pprShellEntry,
      dispatch: {
        "/posts/[id]": {
          kind: "prerender",
          id: "/posts/[id]",
          config: {},
          fallback: { initialRevalidate: 60 },
          pprChain: { headers: { "next-resume": "1" } },
        },
      },
    });

    const res = await dispatchResult(
      { resolvedPathname: "/posts/[id]", invocationTarget: { pathname: "/posts/7" } },
      new Request("https://app.example/posts/7"),
      deps,
    );

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(resumeRequests()[0].method).toBe("POST");
  });

  it("returns 502 when a lambda route has no Function URL", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/api/x": { kind: "lambda", id: "/api/x" } },
      },
      functionUrls: {},
    });

    const res = await dispatchResult(
      { resolvedPathname: "/api/x", invocationTarget: { pathname: "/api/x" } },
      new Request("https://app.example/api/x"),
      deps,
    );

    expect(res.status).toBe(502);
  });

  it("falls back to the R2 asset store when the path is not in the manifest", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({ "/unenumerated.txt": "found" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/unenumerated.txt" },
      new Request("https://app.example/unenumerated.txt"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("found");
  });

  it("returns 404 when neither the manifest nor the R2 asset store has the path", async () => {
    const deps = baseDeps({ assetStore: assetStoreServing({}) });

    const res = await dispatchResult(
      { resolvedPathname: "/missing" },
      new Request("https://app.example/missing"),
      deps,
    );

    expect(res.status).toBe(404);
  });

  it("falls back to the R2 asset store when routing produced no resolved pathname", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({ "/whatever": "asset" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/whatever"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("asset");
  });

  it("answers a middleware-prefetch probe against an unregistered _next/data path with a 200 stub", async () => {
    const deps = baseDeps({ assetStore: assetStoreServing({}) });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/_next/data/test/dashboard.json", {
        headers: { "x-middleware-prefetch": "1" },
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("{}");
    expect(res.headers.get("content-type")).toBe("application/json");
    expect(res.headers.get("x-matched-path")).toBe("/dashboard");
    expect(res.headers.get("x-middleware-skip")).toBe("1");
    expect(res.headers.get("cache-control")).toBe(
      "private, no-cache, no-store, max-age=0, must-revalidate",
    );
  });

  it("still 404s a middleware-prefetch request whose pathname isn't a _next/data path", async () => {
    const deps = baseDeps({ assetStore: assetStoreServing({}) });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/whatever", {
        headers: { "x-middleware-prefetch": "1" },
      }),
      deps,
    );

    expect(res.status).toBe(404);
  });

  it("falls through to the R2 asset store for a _next/data request without x-middleware-prefetch", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({
        "/_next/data/test/dashboard.json": "asset",
      }),
    });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/_next/data/test/dashboard.json"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("asset");
  });

  it("answers a middleware-prefetch probe against a resolved lambda page with a 200 stub, never invoking the lambda", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "test",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/dashboard": { kind: "lambda", id: "/dashboard" } },
      },
      functionUrls: { "/dashboard": "https://fn.example.com" },
      fetch: (async () => {
        throw new Error("should not invoke the origin");
      }) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/dashboard" },
      new Request("https://app.example/_next/data/test/dashboard.json", {
        headers: { "x-middleware-prefetch": "1" },
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("{}");
    expect(res.headers.get("x-matched-path")).toBe("/dashboard");
    expect(res.headers.get("x-middleware-skip")).toBe("1");
  });

  it("does not probe a resolved static or prerender page for a middleware-prefetch request", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "test",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/dashboard": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/dashboard": "<html/>" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/dashboard" },
      new Request("https://app.example/_next/data/test/dashboard.json", {
        headers: { "x-middleware-prefetch": "1" },
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html/>");
  });

  it("emits a redirect response", async () => {
    const res = await dispatchResult(
      { redirect: { url: "https://app.example/new", status: 308 } },
      new Request("https://app.example/old"),
      baseDeps(),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("https://app.example/new");
  });

  it("answers a routing redirect that names no destination", async () => {
    const res = await dispatchResult(
      {
        status: 307,
        resolvedHeaders: new Headers({ Location: "/redirect-dest" }),
      },
      new Request("https://app.example/redirect/a"),
      baseDeps({ assetStore: assetStoreServing({}) }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/redirect-dest");
  });

  it("lets a redirect status win over the page routing went on to resolve", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/about": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/about": "<h1>about</h1>" }),
    });

    const res = await dispatchResult(
      {
        status: 308,
        resolvedHeaders: new Headers({ Location: "/about/" }),
        resolvedPathname: "/about",
      },
      new Request("https://app.example/about"),
      deps,
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/about/");
  });

  it("answers a routing redirect expressed as a Refresh header", async () => {
    const res = await dispatchResult(
      {
        status: 307,
        resolvedHeaders: new Headers({ Refresh: "0;url=/redirect-dest" }),
      },
      new Request("https://app.example/redirect/a"),
      baseDeps({ assetStore: assetStoreServing({}) }),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("refresh")).toBe("0;url=/redirect-dest");
  });

  it("serves the page when a headers() rule sets a location without a redirect status", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/about": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/about": "<h1>about</h1>" }),
    });

    const res = await dispatchResult(
      {
        resolvedHeaders: new Headers({ Location: "/elsewhere" }),
        resolvedPathname: "/about",
      },
      new Request("https://app.example/about"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>about</h1>");
  });

  it("tags a matched route with x-matched-path using the resolved template", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/posts/[id]": { kind: "lambda", id: "/posts/[id]" } },
      },
      functionUrls: { "/posts/[id]": "https://fn.example.com" },
      fetch: (async () => new Response("ok", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/posts/[id]", invocationTarget: { pathname: "/posts/7" } },
      new Request("https://app.example/posts/7"),
      deps,
    );

    expect(res.headers.get("x-matched-path")).toBe("/posts/[id]");
  });

  it("omits x-matched-path when routing produced no resolved pathname", async () => {
    const deps = baseDeps({
      assetStore: assetStoreServing({ "/whatever": "asset" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/whatever"),
      deps,
    );

    expect(res.headers.has("x-matched-path")).toBe(false);
  });
});

describe("routing redirects that name no destination", () => {
  function redirectDeps(
    beforeMiddleware: Route[],
    files: Record<string, string> = {},
    routesOverrides: Record<string, unknown> = {},
  ) {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: Object.keys(files),
        routes: {
          beforeMiddleware,
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
          ...routesOverrides,
        },
        dispatch: Object.fromEntries(
          Object.keys(files).map((path) => [path, { kind: "static" as const }]),
        ),
      },
      assetStore: assetStoreServing(files),
    });
  }

  it("redirects a next.config rule with 307 rather than 404", async () => {
    const res = await serve(
      new Request("https://app.example/redirect/a", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/redirect/a(?:/)?$",
          headers: { Location: "/redirect-dest" },
          status: 307,
        },
      ]),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/redirect-dest");
  });

  it("answers a destination-less user rule that strips a trailing slash", async () => {
    const res = await serve(
      new Request("https://app.example/docs/", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/(.+?)/$",
          headers: { Location: "/$1" },
          missing: [{ type: "header", key: "x-nextjs-data" }],
          status: 308,
        },
      ]),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/docs");
  });

  it("answers a destination-less user rule that adds one, over a page that resolves", async () => {
    const res = await serve(
      new Request("https://app.example/about", { redirect: "manual" }),
      redirectDeps(
        [
          {
            sourceRegex: "^/((?!\\.well-known(?:/.*)?)(?:[^/\\.]+/)*[^/\\.]+)$",
            headers: { Location: "/$1/" },
            missing: [{ type: "header", key: "x-nextjs-data" }],
            status: 308,
          },
        ],
        { "/about": "<h1>about</h1>" },
      ),
    );

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/about/");
  });

  it("stops at the first unconditional redirect match instead of letting a later rule overwrite it", async () => {
    const res = await serve(
      new Request("https://app.example/en/redirect-1", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/en/redirect-1(?:/)?$",
          headers: { Location: "/somewhere/else" },
          status: 307,
        },
        {
          sourceRegex: "^(?:/(en|fr|nl))/redirect-1(?:/)?$",
          headers: { Location: "/$1/somewhere/else" },
          status: 307,
        },
      ]),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/somewhere/else");
  });

  it("truncates the same way for the client-transition data-request pathname", async () => {
    const res = await serve(
      new Request("https://app.example/_next/data/t/en/redirect-1.json", {
        redirect: "manual",
      }),
      redirectDeps(
        [
          {
            sourceRegex: "^/en/redirect-1(?:/)?$",
            headers: { Location: "/somewhere/else" },
            status: 307,
          },
          {
            sourceRegex: "^(?:/(en|fr|nl))/redirect-1(?:/)?$",
            headers: { Location: "/$1/somewhere/else" },
            status: 307,
          },
        ],
        {},
        { shouldNormalizeNextData: true },
      ),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/somewhere/else");
  });

  it("does not truncate on a redirect rule that carries a has/missing condition", async () => {
    const res = await serve(
      new Request("https://app.example/conditional", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/conditional(?:/)?$",
          headers: { Location: "/from-condition" },
          has: [{ type: "header", key: "x-flag" }],
          status: 307,
        },
        {
          sourceRegex: "^/conditional(?:/)?$",
          headers: { Location: "/fallback" },
          status: 307,
        },
      ]),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/fallback");
  });

  it("does not truncate on a rule that does not match this request", async () => {
    const res = await serve(
      new Request("https://app.example/other", { redirect: "manual" }),
      redirectDeps([
        {
          sourceRegex: "^/unrelated(?:/)?$",
          headers: { Location: "/nope" },
          status: 307,
        },
        {
          sourceRegex: "^/other(?:/)?$",
          headers: { Location: "/matched" },
          status: 307,
        },
      ]),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/matched");
  });
});

describe("the service-worker chunk", () => {
  const swPath = "/_next/static/service-worker/sw.js";

  function swDeps(basePath: string) {
    const pathname = `${basePath}${swPath}`;
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath,
        pathnames: [pathname],
        routes: {
          beforeMiddleware: [
            {
              source: `${basePath}/_next/static/service-worker/:path*`,
              sourceRegex: `^${basePath}/_next/static/service-worker(?:/(.*))?(?:/)?$`,
              headers: { "Service-Worker-Allowed": basePath || "/" },
              priority: true,
            },
          ],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        },
        dispatch: { [pathname]: { kind: "static" } },
      },
      assetStore: assetStoreServing({ [pathname]: "self.addEventListener" }),
    });
  }

  it("carries Service-Worker-Allowed from the manifest and a revalidated policy", async () => {
    const res = await serve(new Request(`https://app.example${swPath}`), swDeps(""));

    expect(res.status).toBe(200);
    expect(res.headers.get("service-worker-allowed")).toBe("/");
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
    expect(res.headers.get("content-type")).toBe("text/javascript; charset=utf-8");
  });

  it("carries both under a basePath", async () => {
    const res = await serve(new Request(`https://app.example/docs${swPath}`), swDeps("/docs"));

    expect(res.status).toBe(200);
    expect(res.headers.get("service-worker-allowed")).toBe("/docs");
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });
});

describe("a Server Action's invalidation reaching the colo it travelled through", () => {
  const cfg = { isrPrefix: "prod/p/app/build" };

  function scenario() {
    let now = 10_000;
    let lambdaCalls = 0;
    let actionRevalidates = true;
    let replica = JSON.stringify({
      version: 1,
      deployedAt: 0,
      generatedAt: 900,
      records: {} as Record<string, { expired?: number }>,
    });

    const store = {
      async get(key: string) {
        if (key !== tagSnapshotKey(cfg.isrPrefix)) return null;
        return { etag: '"v"', text: async () => replica };
      },
    };

    const pop = new Map<string, string>();
    const snapshotCache = {
      async match(request: Request) {
        const body = pop.get(request.url);
        return body === undefined ? undefined : new Response(body);
      },
      async put(request: Request, response: Response) {
        pop.set(request.url, await response.text());
      },
      async delete(request: Request) {
        return pop.delete(request.url);
      },
    };

    const colo = new Map<string, Response>();
    const pending: Promise<unknown>[] = [];
    const settle = async () => {
      while (pending.length) await pending.shift();
      await new Promise((resolve) => setTimeout(resolve, 0));
    };

    const deps = baseDeps({
      manifest: {
        buildId: "test",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/blog": {
            kind: "prerender",
            id: "/blog",
            tags: ["posts"],
            config: { renderingMode: "STATIC" },
            fallback: { initialRevalidate: false },
          },
        },
      },
      functionUrls: { "/blog": "https://fn.example.com" },
      fetch: (async (request: Request) => {
        lambdaCalls++;
        if (request.method === "POST") {
          return new Response("action", {
            status: 200,
            headers: actionRevalidates ? { "x-action-revalidated": "1" } : {},
          });
        }
        return new Response("page", {
          status: 200,
          headers: {
            "cache-control": "s-maxage=31536000",
            "x-nextjs-cache": "HIT",
          },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (request: Request) => colo.get(request.url)?.clone(),
          put: async (request: Request, response: Response) => {
            colo.set(request.url, response);
          },
        } as unknown as Cache,
        waitUntil: (promise: Promise<unknown>) => {
          pending.push(promise);
        },
        now: () => now,
      }),
      interception: {
        config: cfg,
        store,
        snapshotCache,
        now: () => now,
        waitUntil: (promise: Promise<unknown>) => {
          pending.push(promise);
        },
      },
    });

    const get = () =>
      dispatchResult(
        { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
        new Request("https://app.example/blog"),
        deps,
      );

    const runAction = () =>
      dispatchResult(
        { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
        new Request("https://app.example/blog", {
          method: "POST",
          headers: { "next-action": "abc" },
        }),
        deps,
      );

    return {
      get,
      runAction,
      settle,
      lambdaCalls: () => lambdaCalls,
      advanceTo: (at: number) => {
        now = at;
      },
      actionRevalidatesNothing: () => {
        actionRevalidates = false;
      },
      invalidate: (at: number) => {
        replica = JSON.stringify({
          version: 1,
          deployedAt: 0,
          generatedAt: at,
          records: { posts: { expired: at } },
        });
      },
    };
  }

  it("renders the invalidated entry at the origin on the next request", async () => {
    const s = scenario();

    await s.get();
    await s.settle();
    expect(s.lambdaCalls()).toBe(1);

    s.invalidate(20_000);
    s.advanceTo(15_000);
    const action = await s.runAction();
    expect(action.headers.get("x-action-revalidated")).toBe("1");
    await s.settle();
    expect(s.lambdaCalls()).toBe(2);

    s.advanceTo(30_000);
    const after = await s.get();

    expect(after.headers.get("x-ocel-cache")).toBe("MISS");
    await s.settle();
    expect(s.lambdaCalls()).toBe(3);
  });

  it("keeps answering from the cached replica when the action revalidated nothing", async () => {
    const s = scenario();
    s.actionRevalidatesNothing();

    await s.get();
    await s.settle();

    s.invalidate(20_000);
    s.advanceTo(15_000);
    const action = await s.runAction();
    expect(action.headers.has("x-action-revalidated")).toBe(false);
    await s.settle();

    s.advanceTo(30_000);
    const after = await s.get();

    expect(after.headers.get("x-nextjs-cache")).toBe("HIT");
  });
});

describe("data-request invocation pathname", () => {
  function lambdaDeps(
    manifest: Partial<RouteDeps["manifest"]> = {},
  ): { deps: RouteDeps; invoked: () => URL } {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/[...route]": { kind: "lambda", id: "fn", entryKey: "e" } },
        ...manifest,
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });
    return { deps, invoked: () => captured! };
  }

  it("forwards a _next/data request to the lambda under its data pathname", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("keeps the locale prefix on a data pathname", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/en/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/en/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/en/middleware/works.json");
  });

  it("maps the root invocation pathname back to index.json", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      { resolvedPathname: "/[...route]", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/_next/data/t/index.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/index.json");
  });

  it("drops a trailingSlash app's trailing slash from the data pathname", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works/" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("wraps the data pathname under the app's basePath", async () => {
    const { deps, invoked } = lambdaDeps({ basePath: "/docs" });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/docs/middleware/works" },
      },
      new Request("https://app.example/docs/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe(
      "/docs/_next/data/t/middleware/works.json",
    );
  });

  it("does not treat a lookalike prefix as the app's basePath", async () => {
    const { deps, invoked } = lambdaDeps({ basePath: "/docs" });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/docsy/works" },
      },
      new Request("https://app.example/docs/_next/data/t/docsy/works.json"),
      deps,
    );

    expect(invoked().pathname).toBe("/docs/_next/data/t/docsy/works.json");
  });

  it("invokes an edge route with the data-wrapped pathname", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/[...route]": { kind: "edge", entryKey: "middleware_app/edge" },
        },
      },
      edge: async (_entryKey, request) => {
        captured = new URL(request.url);
        return new Response("{}", { status: 200 });
      },
    });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(captured!.pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("renders a prerendered route under the data pathname", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/[...route]": { kind: "prerender", id: "fn", config: {} },
        },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(captured!.pathname).toBe("/_next/data/t/middleware/works.json");
  });

  it("preserves the query string of a data request", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json?a=1"),
      deps,
    );

    expect(invoked().pathname).toBe("/_next/data/t/middleware/works.json");
    expect(invoked().search).toBe("?a=1");
  });

  it("forwards the query resolveRoutes merged onto invocationTarget, not just the client's own search string", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: {
          pathname: "/middleware/works",
          query: { a: "b", foo: "bar" },
        },
      },
      new Request("https://app.example/middleware/works?a=b"),
      deps,
    );

    expect(invoked().search).toBe("?a=b&foo=bar");
  });

  it("falls back to the client's own search string when invocationTarget carries no query", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/middleware/works?a=b"),
      deps,
    );

    expect(invoked().search).toBe("?a=b");
  });

  it("leaves a document request's invocation pathname untouched", async () => {
    const { deps, invoked } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/middleware/works"),
      deps,
    );

    expect(invoked().pathname).toBe("/middleware/works");
  });

  it("does not double-wrap an already-data invocation pathname", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/_next/data/t/index.json": {
            kind: "prerender",
            id: "fn",
            config: {},
          },
        },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/_next/data/t/index.json",
        invocationTarget: { pathname: "/_next/data/t/index.json" },
      },
      new Request("https://app.example/_next/data/t/index.json"),
      deps,
    );

    expect(captured!.pathname).toBe("/_next/data/t/index.json");
  });
});

describe("x-nextjs-data on the origin forward", () => {
  function lambdaDeps(): { deps: RouteDeps; headers: () => Headers } {
    let captured: Headers | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/[...route]": { kind: "lambda", id: "fn", entryKey: "e" } },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req.headers;
        return new Response("{}", { status: 200 });
      }) as unknown as typeof fetch,
    });
    return { deps, headers: () => captured! };
  }

  it("stamps x-nextjs-data on a genuine data request's origin forward", async () => {
    const { deps, headers } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/_next/data/t/middleware/works.json"),
      deps,
    );

    expect(headers().get("x-nextjs-data")).toBe("1");
  });

  it("does not stamp it on a document request's origin forward", async () => {
    const { deps, headers } = lambdaDeps();

    await dispatchResult(
      {
        resolvedPathname: "/[...route]",
        invocationTarget: { pathname: "/middleware/works" },
      },
      new Request("https://app.example/middleware/works"),
      deps,
    );

    expect(headers().has("x-nextjs-data")).toBe(false);
  });
});

describe("decode failures on a routed dynamic param", () => {
  it("answers 400 when a routeMatches value fails to decode", async () => {
    const res = await dispatchResult(
      {
        resolvedPathname: "/[id]",
        routeMatches: { "1": "%2", nxtPid: "%2" },
        invocationTarget: { pathname: "/%2" },
      },
      new Request("https://app.example/%2"),
      baseDeps(),
    );

    expect(res.status).toBe(400);
  });

  it("does not 400 when every routeMatches value decodes cleanly", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/[id]": { kind: "static" } },
      },
      assetStore: assetStoreServing({ "/[id].html": "doc" }),
    });

    const res = await dispatchResult(
      {
        resolvedPathname: "/[id]",
        routeMatches: { "1": "hello" },
        invocationTarget: { pathname: "/hello" },
      },
      new Request("https://app.example/hello"),
      deps,
    );

    expect(res.status).toBe(200);
  });

  it("still 404s an unmatched path rather than 400ing it", async () => {
    const res = await dispatchResult(
      {},
      new Request("https://app.example/%2"),
      baseDeps({ assetStore: assetStoreServing({}) }),
    );

    expect(res.status).not.toBe(400);
  });

  it("redirects rather than 400ing a decode failure the routing result also redirects", async () => {
    const res = await dispatchResult(
      {
        resolvedPathname: "/[id]",
        routeMatches: { "1": "%2" },
        invocationTarget: { pathname: "/%2" },
        status: 307,
        resolvedHeaders: new Headers({ location: "/dest" }),
      },
      new Request("https://app.example/%2", { redirect: "manual" }),
      baseDeps(),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/dest");
  });
});

describe("an afterFiles rewrite shadowed by a dynamic route", () => {
  function shadowDeps() {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/ssr-page", "/[id]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [
            { sourceRegex: "^/rewrite-1(?:/)?$", destination: "/ssr-page?from=config" },
          ],
          dynamicRoutes: [
            { sourceRegex: "^[/]?/(?<nxtPid>[^/]+?)(?:/)?$", destination: "/[id]?nxtPid=$nxtPid" },
          ],
          onMatch: [],
          fallback: [],
        },
        dispatch: {
          "/ssr-page": { kind: "lambda", id: "/ssr-page" },
          "/[id]": { kind: "static" },
        },
      },
      functionUrls: { "/ssr-page": "https://fn.example.com" },
      fetch: (async () => new Response("ssr-page rendered", { status: 200 })) as unknown as typeof fetch,
      assetStore: assetStoreServing({ "/[id].html": "dynamic route doc" }),
    });
  }

  it("serves the page the rewrite named rather than the dynamic route @next/routing shadows it with", async () => {
    const res = await serve(new Request("https://app.example/rewrite-1"), shadowDeps());

    expect(res.headers.get("x-matched-path")).toBe("/ssr-page");
    expect(await res.text()).toBe("ssr-page rendered");
  });

  it("still serves the dynamic route when the rewrite's destination is not itself a build pathname", async () => {
    const deps = shadowDeps();
    deps.manifest.routes = {
      ...(deps.manifest.routes as object),
      afterFiles: [{ sourceRegex: "^/rewrite-1(?:/)?$", destination: "/foo" }],
    } as typeof deps.manifest.routes;

    const res = await serve(new Request("https://app.example/rewrite-1"), deps);

    expect(res.headers.get("x-matched-path")).toBe("/[id]");
    expect(await res.text()).toBe("dynamic route doc");
  });

  function encodedLiteralDeps() {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/dynamic/[slug]", "/dynamic/[first]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/dynamic/(?<nxtPslug>[^/]+?)(?:/)?$",
              destination: "/dynamic/[slug]?nxtPslug=$nxtPslug",
            },
          ],
          onMatch: [],
          fallback: [],
        },
        dispatch: {
          "/dynamic/[slug]": { kind: "static" },
          "/dynamic/[first]": { kind: "static" },
        },
      },
      assetStore: assetStoreServing({
        "/dynamic/[slug].html": "template doc",
        "/dynamic/[first].html": "prerendered doc",
      }),
    });
  }

  it("serves the prerendered literal an encoded request target names", async () => {
    const res = await serve(
      new Request("https://app.example/dynamic/%5Bfirst%5D"),
      encodedLiteralDeps(),
    );

    expect(res.headers.get("x-matched-path")).toBe("/dynamic/[first]");
    expect(await res.text()).toBe("prerendered doc");
  });

  it("still serves the template when the decoded target is no build pathname", async () => {
    const res = await serve(
      new Request("https://app.example/dynamic/%5Bsecond%5D"),
      encodedLiteralDeps(),
    );

    expect(res.headers.get("x-matched-path")).toBe("/dynamic/[slug]");
    expect(await res.text()).toBe("template doc");
  });

  it("leaves a redirect result alone rather than stamping x-matched-path on it", async () => {
    const deps = shadowDeps();
    deps.manifest.middleware = { runtime: "edge", entryKey: "mw" };
    deps.edge = async () =>
      new Response(null, { status: 307, headers: { location: "/elsewhere" } });

    const res = await serve(
      new Request("https://app.example/ssr-page", { redirect: "manual" }),
      deps,
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("/elsewhere");
    expect(res.headers.has("x-matched-path")).toBe(false);
  });

  it("uses the exact page's own prerender config after the swap, not the shadowing template's", async () => {
    let captured: { host: string; headers: Headers } | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/ssg/hello", "/ssg/[slug]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [
            { sourceRegex: "^/to-ssg(?:/)?$", destination: "/ssg/hello" },
          ],
          dynamicRoutes: [
            {
              sourceRegex: "^/ssg/(?<nxtPslug>[^/]+?)(?:/)?$",
              destination: "/ssg/[slug]?nxtPslug=$nxtPslug",
            },
          ],
          onMatch: [],
          fallback: [],
        },
        dispatch: {
          "/ssg/hello": {
            kind: "prerender",
            id: "hello-fn",
            config: { allowHeader: ["x-marker-hello"], allowQuery: ["keep-hello"] },
          },
          "/ssg/[slug]": {
            kind: "prerender",
            id: "slug-fn",
            config: { allowHeader: ["x-marker-slug"], allowQuery: ["keep-slug"] },
          },
        },
      },
      functionUrls: {
        "hello-fn": "https://hello.example.com",
        "slug-fn": "https://slug.example.com",
      },
      cache: coloDeps({
        cache: {
          match: async () => undefined,
          put: async () => {},
        } as unknown as Cache,
        waitUntil: () => {},
      }),
      fetch: (async (req: Request) => {
        captured = { host: new URL(req.url).host, headers: req.headers };
        return new Response("rendered", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await serve(
      new Request("https://app.example/to-ssg", {
        headers: { "x-marker-hello": "1", "x-marker-slug": "1" },
      }),
      deps,
    );

    expect(res.headers.get("x-matched-path")).toBe("/ssg/hello");
    expect(captured?.host).toBe("hello.example.com");
    expect(captured?.headers.get("x-marker-hello")).toBe("1");
    expect(captured?.headers.has("x-marker-slug")).toBe(false);
  });
});

describe("a concrete route shadowed by a dynamic sibling", () => {
  function apiDeps(): { deps: RouteDeps; invoked: () => URL } {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/api/hello", "/api/[id]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^[/]?/api/(?<nxtPid>[^/]+?)$",
              destination: "/api/[id]?nxtPid=$nxtPid",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/api/hello": { kind: "lambda", id: "/api/hello" },
          "/api/[id]": { kind: "lambda", id: "/api/[id]" },
        },
      },
      functionUrls: {
        "/api/hello": "https://hello.example.com",
        "/api/[id]": "https://id.example.com",
      },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });
    return { deps, invoked: () => captured! };
  }

  it("drops the shadowing route's params from a concrete route's forwarded query", async () => {
    const { deps, invoked } = apiDeps();

    await serve(new Request("https://app.example/api/hello?a=b"), deps);

    expect(invoked().searchParams.has("nxtPid")).toBe(false);
    expect(invoked().searchParams.get("a")).toBe("b");
  });

  it("drops the shadowing route's params from a concrete _next/data request", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/", "/_next/data/t/index.json", "/[id]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^[/]?/(?<nxtPid>[^/]+?)$",
              destination: "/[id]?nxtPid=$nxtPid",
            },
            {
              sourceRegex: "^/_next/data/t/(?<nxtPid>[^/]+?)\\.json$",
              destination: "/[id]?nxtPid=$nxtPid",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/": { kind: "lambda", id: "/" },
          "/_next/data/t/index.json": { kind: "lambda", id: "/" },
          "/[id]": { kind: "lambda", id: "/[id]" },
        },
      },
      functionUrls: {
        "/": "https://root.example.com",
        "/[id]": "https://id.example.com",
      },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await serve(new Request("https://app.example/_next/data/t/index.json"), deps);

    expect(captured?.searchParams.has("nxtPid")).toBe(false);
  });

  it("still carries params for a genuinely dynamic request", async () => {
    const { deps, invoked } = apiDeps();

    await serve(new Request("https://app.example/api/something-else"), deps);

    expect(invoked().searchParams.get("nxtPid")).toBe("something-else");
  });

  it("keeps params for a prerendered concrete path of a dynamic page", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/blog/post-1", "/blog/[slug]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^[/]?/blog/(?<nxtPslug>[^/]+?)$",
              destination: "/blog/[slug]?nxtPslug=$nxtPslug",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/blog/post-1": { kind: "prerender", id: "/blog/post-1", config: {} },
        },
      },
      functionUrls: { "/blog/post-1": "https://blog.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("rendered", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await serve(new Request("https://app.example/blog/post-1"), deps);

    expect(captured?.searchParams.get("nxtPslug")).toBe("post-1");
  });

  it("leaves the root path unaffected", async () => {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/", "/[id]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^[/]?/(?<nxtPid>[^/]+?)$",
              destination: "/[id]?nxtPid=$nxtPid",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: { "/": { kind: "lambda", id: "/" } },
      },
      functionUrls: { "/": "https://root.example.com" },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    const res = await serve(new Request("https://app.example/"), deps);

    expect(res.status).toBe(200);
    expect(captured?.searchParams.has("nxtPid")).toBe(false);
  });
});

describe("the origin URL under a config rewrite", () => {
  function capturingDeps(overrides: Partial<RouteDeps["manifest"]> = {}): {
    deps: RouteDeps;
    invoked: () => URL;
  } {
    let captured: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/blog/[post]", "/rewrite-target"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/blog/(?<nxtPpost>[^/]+?)(?:/)?$",
              destination: "/blog/[post]?nxtPpost=$nxtPpost",
            },
          ],
          onMatch: [],
          fallback: [],
        },
        dispatch: {
          "/blog/[post]": { kind: "lambda", id: "blog" },
          "/rewrite-target": { kind: "lambda", id: "target" },
        },
        ...overrides,
      },
      functionUrls: {
        blog: "https://blog.example.com",
        target: "https://target.example.com",
      },
      fetch: (async (req: Request) => {
        captured = new URL(req.url);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });
    return { deps, invoked: () => captured! };
  }

  it("forwards the rewrite's source pathname and query, not the destination", async () => {
    const { deps, invoked } = capturingDeps({
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [{ sourceRegex: "^/blog-post-1$", destination: "/blog/post-1" }],
        dynamicRoutes: [
          {
            sourceRegex: "^/blog/(?<nxtPpost>[^/]+?)(?:/)?$",
            destination: "/blog/[post]?nxtPpost=$nxtPpost",
          },
        ],
        onMatch: [],
        fallback: [],
      } as unknown as RouteDeps["manifest"]["routes"],
    });

    await serve(new Request("https://app.example/blog-post-1?ref=home"), deps);

    expect(invoked().pathname).toBe("/blog-post-1");
    expect(invoked().searchParams.get("ref")).toBe("home");
    expect(invoked().searchParams.has("nxtPpost")).toBe(false);
  });

  it("still forwards the interpolated pathname for a direct dynamic-route hit", async () => {
    const { deps, invoked } = capturingDeps();

    await serve(new Request("https://app.example/blog/post-1"), deps);

    expect(invoked().pathname).toBe("/blog/post-1");
  });

  it("does not leak a rewrite's synthesized destination query onto the origin request", async () => {
    const { deps, invoked } = capturingDeps({
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [
          {
            sourceRegex: "^/rewrite-source/(.+)$",
            destination: "/rewrite-target?path=$1",
          },
        ],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      } as unknown as RouteDeps["manifest"]["routes"],
    });

    await serve(new Request("https://app.example/rewrite-source/foo"), deps);

    expect(invoked().pathname).toBe("/rewrite-source/foo");
    expect(invoked().searchParams.has("path")).toBe(false);
  });
});

describe("the colo cache key under a config rewrite", () => {
  it("keys two source URLs that rewrite to the same destination separately", async () => {
    const seen: string[] = [];
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/bar"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [
            { sourceRegex: "^/foo1$", destination: "/bar" },
            { sourceRegex: "^/foo2$", destination: "/bar" },
          ],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/bar": {
            kind: "prerender",
            id: "bar",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { bar: "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (req: Request) => {
            seen.push(req.url);
            return undefined;
          },
          put: async (req: Request) => {
            seen.push(req.url);
          },
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => pending.push(p),
      }),
    });

    await serve(new Request("https://app.example/foo1"), deps);
    await serve(new Request("https://app.example/foo2"), deps);
    await Promise.all(pending);

    expect(seen.some((k) => k.endsWith("/foo1"))).toBe(true);
    expect(seen.some((k) => k.endsWith("/foo2"))).toBe(true);
    expect(seen.some((k) => k.endsWith("/bar"))).toBe(false);
  });

  it("keys two concrete paths of the same dynamic route separately, not by the route pattern", async () => {
    const seen: string[] = [];
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/blog/[slug]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/blog/(?<nxtPslug>[^/]+?)(?:/)?$",
              destination: "/blog/[slug]?nxtPslug=$nxtPslug",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/blog/[slug]": {
            kind: "prerender",
            id: "blog",
            config: {},
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { blog: "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (req: Request) => {
            seen.push(req.url);
            return undefined;
          },
          put: async (req: Request) => {
            seen.push(req.url);
          },
        } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => pending.push(p),
      }),
    });

    await serve(new Request("https://app.example/blog/post-1"), deps);
    await serve(new Request("https://app.example/blog/post-2"), deps);
    await Promise.all(pending);

    expect(seen.some((k) => k.endsWith("/blog/post-1"))).toBe(true);
    expect(seen.some((k) => k.endsWith("/blog/post-2"))).toBe(true);
    expect(seen.some((k) => k.includes("[slug]"))).toBe(false);
  });
});

describe("a fallback path of a dynamic route's ISR revalidation", () => {
  it("reads the store at the concrete requested path and enqueues that same concrete path, not the route pattern", async () => {
    const isrPrefix = "prod/p/app/build";
    const storeReads: string[] = [];
    const sent: unknown[] = [];
    const pending: Promise<unknown>[] = [];
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/posts/[id]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/posts/(?<nxtPid>[^/]+?)(?:/)?$",
              destination: "/posts/[id]?nxtPid=$nxtPid",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/posts/[id]": {
            kind: "prerender",
            id: "posts",
            entryKey: "app/posts/[id]/page",
            config: { bypassToken: "TOKEN" },
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { posts: "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: coloDeps({
        cache: { match: async () => undefined, put: async () => {} } as unknown as Cache,
        waitUntil: (p: Promise<unknown>) => pending.push(p),
        enqueueRevalidation: async (message) => {
          sent.push(message);
          return true;
        },
      }),
      interception: {
        config: { isrPrefix },
        now: () => 1_000 + 61_000,
        store: {
          async get(key: string) {
            storeReads.push(key);
            if (key !== `${isrPrefix}/cache/posts/7.cache.json`) return null;
            return {
              text: async () =>
                JSON.stringify({
                  lastModified: 1_000,
                  value: {
                    kind: "APP_PAGE",
                    html: "<html>post-7</html>",
                    status: 200,
                    headers: {},
                  },
                }),
            };
          },
        },
      },
    });

    const res = await serve(new Request("https://app.example/posts/7"), deps);
    await Promise.all(pending);

    expect(await res.text()).toBe("<html>post-7</html>");
    expect(storeReads).toContain("prod/p/app/build/cache/posts/7.cache.json");
    expect(storeReads).not.toContain("prod/p/app/build/cache/posts/[id].cache.json");
    expect(sent).toEqual([
      expect.objectContaining({ routePath: "/posts/7" }),
    ]);
  });
});

describe("a generated interception rewrite's prerender key", () => {
  const isrPrefix = "prod/p/app/build";
  const entryKey = "(.)test-nested";

  function interceptionDeps(overrides: { lambdaBody?: string } = {}) {
    let lambda = 0;
    return {
      deps: baseDeps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/test-nested", "/(.)test-nested"],
          routes: {
            beforeMiddleware: [],
            beforeFiles: [
              {
                sourceRegex: "^/test-nested$",
                destination: "/(.)test-nested",
                has: [{ type: "header", key: "next-url" }],
              },
            ],
            afterFiles: [],
            dynamicRoutes: [],
            onMatch: [],
            fallback: [],
          } as unknown as RouteDeps["manifest"]["routes"],
          dispatch: {
            "/(.)test-nested": {
              kind: "prerender",
              id: "test-nested",
              config: {},
              fallback: { initialRevalidate: 60 },
            },
          },
        },
        functionUrls: { "test-nested": "https://fn.example.com" },
        fetch: (async () => {
          lambda++;
          return new Response(overrides.lambdaBody ?? "from-lambda", {
            status: 200,
            headers: { "cache-control": "s-maxage=60" },
          });
        }) as unknown as typeof fetch,
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
          store: {
            async get(key: string) {
              if (key !== `${isrPrefix}/cache/${entryKey}.cache.json`) return null;
              return {
                text: async () =>
                  JSON.stringify({
                    lastModified: 1_000,
                    value: {
                      kind: "APP_PAGE",
                      html: "<html>intercepted</html>",
                      status: 200,
                      headers: {},
                      segmentData: { "/children": btoa("INTERCEPTED-SEGMENT") },
                      segmentHeaders: { "content-type": "text/x-component" },
                    },
                  }),
              };
            },
          },
        },
      }),
      lambdaCalls: () => lambda,
    };
  }

  it("resolves a segment-prefetch request's ISR key to the rewritten path, not the requested one", async () => {
    const { deps, lambdaCalls } = interceptionDeps();

    const res = await serve(
      new Request("https://app.example/test-nested", {
        headers: {
          RSC: "1",
          "next-url": "/",
          "next-router-segment-prefetch": "/children",
        },
      }),
      deps,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await res.text()).toBe("INTERCEPTED-SEGMENT");
    expect(lambdaCalls()).toBe(0);
  });

  it("still forwards the requested (source) path to the origin, unaffected by the ISR key fix", async () => {
    let capturedPath: string | undefined;
    const { deps } = interceptionDeps();
    deps.fetch = (async (req: Request) => {
      capturedPath = new URL(req.url).pathname;
      return new Response("miss", { status: 404 });
    }) as unknown as typeof fetch;
    deps.interception!.store = { async get() { return null; } };

    await serve(
      new Request("https://app.example/test-nested", {
        headers: {
          RSC: "1",
          "next-url": "/",
          "next-router-segment-prefetch": "/children",
        },
      }),
      deps,
    );

    expect(capturedPath).toBe("/test-nested");
  });
});

describe("an origin that cannot answer a segment prefetch", () => {
  function scenario(originHeaders: Record<string, string>) {
    let lambdaCalls = 0;
    const colo = new Map<string, Response>();
    const pending: Promise<unknown>[] = [];
    const settle = async () => {
      while (pending.length) await pending.shift();
      await new Promise((resolve) => setTimeout(resolve, 0));
    };

    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/settings"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: {
          "/settings": {
            kind: "prerender",
            id: "settings",
            config: { renderingMode: "PARTIALLY_STATIC" },
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      functionUrls: { settings: "https://fn.example.com" },
      fetch: (async () => {
        lambdaCalls++;
        return new Response("SHELL-OR-SEGMENT", {
          status: 200,
          headers: { "cache-control": "s-maxage=60", ...originHeaders },
        });
      }) as unknown as typeof fetch,
      cache: coloDeps({
        cache: {
          match: async (request: Request) => colo.get(request.url)?.clone(),
          put: async (request: Request, response: Response) => {
            colo.set(request.url, response);
          },
        } as unknown as Cache,
        waitUntil: (promise: Promise<unknown>) => {
          pending.push(promise);
        },
        now: () => 1_000,
      }),
    });

    const prefetch = () =>
      serve(
        new Request("https://app.example/settings", {
          headers: {
            RSC: "1",
            "next-router-prefetch": "1",
            "next-router-segment-prefetch": "/$d$team/$d$project/settings",
          },
        }),
        deps,
      );

    return { prefetch, settle, colo, lambda: () => lambdaCalls };
  }

  it("answers a 204 miss instead of handing the client a postponed shell", async () => {
    const { prefetch, settle } = scenario({ "x-nextjs-postponed": "1" });

    const res = await prefetch();
    await settle();

    expect(res.status).toBe(204);
    expect(await res.text()).toBe("");
  });

  it("stores nothing under the segment key, so the next prefetch is not latched to the shell", async () => {
    const { prefetch, settle, colo, lambda } = scenario({
      "x-nextjs-postponed": "1",
    });

    await (await prefetch()).text();
    await settle();
    const second = await prefetch();
    await settle();

    expect([...colo.keys()]).toEqual([]);
    expect(second.status).toBe(204);
    expect(lambda()).toBe(2);
  });

  it("still caches and serves a real segment payload", async () => {
    const { prefetch, settle, colo, lambda } = scenario({
      "x-nextjs-postponed": "2",
    });

    const first = await prefetch();
    expect(await first.text()).toBe("SHELL-OR-SEGMENT");
    await settle();

    const second = await prefetch();

    expect([...colo.keys()]).toEqual([
      "https://cache.ocel/p1/web/d1/settings.segments/%2F%24d%24team%2F%24d%24project%2Fsettings.segment.rsc",
    ]);
    expect(second.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await second.text()).toBe("SHELL-OR-SEGMENT");
    expect(lambda()).toBe(1);
  });
});

describe("the x-vercel-cache alias", () => {
  const emptyRoutes = {
    beforeMiddleware: [],
    beforeFiles: [],
    afterFiles: [],
    dynamicRoutes: [],
    onMatch: [],
    fallback: [],
  };

  const isrPrefix = "prod/p/app/build";

  const missingColo = () =>
    coloDeps({
      cache: {
        match: async () => undefined,
        put: async () => {},
      } as unknown as Cache,
      waitUntil: () => {},
    });

  function aliasDeps(vercelCacheAlias?: boolean): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/"],
        routes: emptyRoutes,
        dispatch: { "/": { kind: "prerender", id: "/", config: {} } },
        ...(vercelCacheAlias !== undefined && { vercelCacheAlias }),
      },
      functionUrls: { "/": "https://fn.example.com" },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
      cache: missingColo(),
    });
  }

  it("stamps the tier that answered under Vercel's name", async () => {
    const res = await serve(new Request("https://app.example/"), aliasDeps(true));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(res.headers.get("x-vercel-cache")).toBe("MISS");
    expect(await res.text()).toBe("rendered");
  });

  it("emits nothing for a build that did not ask for it", async () => {
    const res = await serve(new Request("https://app.example/"), aliasDeps());

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(res.headers.get("x-vercel-cache")).toBeNull();
  });

  it("stamps a composed PPR shell, which never passes through withStatus", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/ppr"],
        routes: emptyRoutes,
        dispatch: {
          "/ppr": {
            kind: "prerender",
            id: "/ppr",
            config: {},
            fallback: { initialRevalidate: 60, initialExpiration: 3600 },
            pprChain: { headers: { "next-resume": "1" } },
          },
        },
        vercelCacheAlias: true,
      },
      functionUrls: { "/ppr": "https://fn.example.com" },
      fetch: (async () => new Response("[dynamic]", { status: 200 })) as unknown as typeof fetch,
      cache: missingColo(),
      interception: {
        config: { isrPrefix },
        now: () => 2_000,
        store: {
          async get(key: string) {
            if (key !== `${isrPrefix}/cache/ppr.cache.json`) return null;
            return {
              text: async () =>
                JSON.stringify({
                  lastModified: 1_000,
                  value: {
                    kind: "APP_PAGE",
                    html: "[shell]",
                    postponed: "POSTPONED",
                    status: 200,
                    headers: {},
                  },
                }),
            };
          },
        },
      },
    });

    const res = await serve(new Request("https://app.example/ppr"), deps);

    expect(await res.text()).toBe("[shell][dynamic]");
    expect(res.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(res.headers.get("x-vercel-cache")).toBe("PRERENDER");
  });
});

describe("custom error page substitution", () => {
  function errorPageDeps(
    pageResponse: () => Response,
    overrides: Partial<RouteDeps> = {},
  ): RouteDeps {
    return baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/not-found", "/404", "/500"],
        routes: {},
        dispatch: {
          "/not-found": { kind: "lambda", id: "page", entryKey: "/not-found", page: true },
          "/404": { kind: "lambda", id: "page", entryKey: "/404", page: true },
          "/500": { kind: "lambda", id: "page", entryKey: "/500", page: true },
        },
        errorRoutes: { notFound: "/404", serverError: "/500" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        const entryKey = req.headers.get("x-ocel-entry");
        if (entryKey === "/not-found") return pageResponse();
        if (entryKey === "/404") {
          return new Response("<html>custom 404</html>", {
            status: 200,
            headers: { "content-type": "text/html" },
          });
        }
        if (entryKey === "/500") {
          return new Response("<html>custom 500</html>", {
            status: 200,
            headers: { "content-type": "text/html" },
          });
        }
        throw new Error(`unexpected entry ${entryKey}`);
      }) as unknown as typeof fetch,
      ...overrides,
    });
  }

  it("substitutes the /404 entry's body for a 404 document response, keeping the 404 status", async () => {
    const deps = errorPageDeps(
      () => new Response("This page could not be found", { status: 404 }),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/not-found"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<html>custom 404</html>");
  });

  it("substitutes the /500 entry's body for a 5xx document response, keeping the original status", async () => {
    const deps = errorPageDeps(
      () => new Response("Internal Server Error", { status: 500 }),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/enoent"),
      deps,
    );

    expect(res.status).toBe(500);
    expect(await res.text()).toBe("<html>custom 500</html>");
  });

  it("leaves a 404 _next/data response untouched", async () => {
    const deps = errorPageDeps(
      () =>
        new Response(JSON.stringify({ notFound: true }), {
          status: 404,
          headers: { "content-type": "application/json" },
        }),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/_next/data/t/not-found.json"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe(JSON.stringify({ notFound: true }));
  });

  it("still 404s a genuinely missing static asset without substitution", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {},
        errorRoutes: { notFound: "/404", serverError: "/500" },
      },
      assetStore: assetStoreServing({}),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/missing-asset.txt" },
      new Request("https://app.example/missing-asset.txt"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).not.toBe("<html>custom 404</html>");
  });

  it("does not substitute a 404 from an API route", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/api/missing", "/404"],
        routes: {},
        dispatch: {
          "/api/missing": { kind: "lambda", id: "api", entryKey: "/api/missing" },
          "/404": { kind: "lambda", id: "page", entryKey: "/404", page: true },
        },
        errorRoutes: { notFound: "/404" },
      },
      functionUrls: { api: "https://fn.example.com", page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("Not Found", { status: 404 })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/api/missing", invocationTarget: { pathname: "/api/missing" } },
      new Request("https://app.example/api/missing"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
  });

  it("passes through an origin 500 that already rendered a text/html document", async () => {
    const deps = errorPageDeps(
      () =>
        new Response("<html id=\"__next_error__\">origin error boundary</html>", {
          status: 500,
          headers: { "content-type": "text/html; charset=utf-8", "x-origin": "1" },
        }),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/enoent"),
      deps,
    );

    expect(res.status).toBe(500);
    expect(res.headers.get("x-origin")).toBe("1");
    expect(await res.text()).toBe(
      "<html id=\"__next_error__\">origin error boundary</html>",
    );
  });

  it("passes through an origin 404 from a matched lambda+page target that already rendered a document", async () => {
    const deps = errorPageDeps(
      () =>
        new Response("<html id=\"__next_error__\">route-specific not found</html>", {
          status: 404,
          headers: { "content-type": "text/html; charset=utf-8", "x-origin": "1" },
        }),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/de/show"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(res.headers.get("x-origin")).toBe("1");
    expect(await res.text()).toBe(
      "<html id=\"__next_error__\">route-specific not found</html>",
    );
  });

  it("still substitutes a bodiless plaintext origin 500 with no content-type", async () => {
    const deps = errorPageDeps(
      () => new Response("Internal Server Error", { status: 500 }),
    );

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/enoent"),
      deps,
    );

    expect(res.status).toBe(500);
    expect(await res.text()).toBe("<html>custom 500</html>");
  });

  it("substitutes a static-kind error route's body for a document error response", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/not-found", "/500"],
        routes: {},
        dispatch: {
          "/not-found": { kind: "lambda", id: "page", entryKey: "/not-found", page: true },
          "/500": { kind: "static" },
        },
        errorRoutes: { serverError: "/500" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("Internal Server Error", { status: 500 })) as unknown as typeof fetch,
      assetStore: assetStoreServing({ "/500.html": "<html>static 500</html>" }),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/not-found", invocationTarget: { pathname: "/not-found" } },
      new Request("https://app.example/not-found"),
      deps,
    );

    expect(res.status).toBe(500);
    expect(await res.text()).toBe("<html>static 500</html>");
  });
});

describe("not-found for an unresolved pathname under a flight header", () => {
  const flightBody = '1:"$Sreact.fragment"\n';

  function notFoundDeps(overrides: { flightRoute?: boolean } = {}) {
    const entries: string[] = [];
    return {
      entries: () => entries,
      deps: baseDeps({
        manifest: {
          buildId: "t",
          basePath: "",
          pathnames: ["/404"],
          routes: {},
          dispatch: {
            "/404": { kind: "static" },
            "/_not-found": {
              kind: "lambda",
              id: "page",
              entryKey: "/_not-found",
              page: true,
            },
          },
          errorRoutes: {
            notFound: "/404",
            ...(overrides.flightRoute === false
              ? {}
              : { notFoundFlight: "/_not-found" }),
          },
        },
        functionUrls: { page: "https://fn.example.com" },
        assetStore: assetStoreServing({ "/404.html": "<html>static 404</html>" }),
        fetch: (async (req: Request) => {
          entries.push(req.headers.get("x-ocel-entry") ?? "");
          return new Response(flightBody, {
            status: 200,
            headers: { "content-type": "text/x-component" },
          });
        }) as unknown as typeof fetch,
      }),
    };
  }

  const unresolved = (headers?: Record<string, string>) =>
    new Request("https://app.example/en/gsp/stories/dynamic-123?_rsc=p", {
      headers,
    });

  it("renders the flight not-found route instead of the prerendered HTML 404", async () => {
    const { deps, entries } = notFoundDeps();

    const res = await dispatchResult({}, unresolved({ rsc: "1" }), deps);

    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toBe("text/x-component");
    expect(await res.text()).toBe(flightBody);
    expect(entries()).toEqual(["/_not-found"]);
  });

  it("still serves the prerendered HTML 404 to a document request", async () => {
    const { deps, entries } = notFoundDeps();

    const res = await dispatchResult({}, unresolved(), deps);

    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await res.text()).toBe("<html>static 404</html>");
    expect(entries()).toEqual([]);
  });

  it("serves the HTML 404 when the build names no flight not-found route", async () => {
    const { deps, entries } = notFoundDeps({ flightRoute: false });

    const res = await dispatchResult({}, unresolved({ rsc: "1" }), deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<html>static 404</html>");
    expect(entries()).toEqual([]);
  });

  it("leaves a missing /_next/static asset a plain 404, flight header or not", async () => {
    const { deps, entries } = notFoundDeps();

    const res = await dispatchResult(
      {},
      new Request("https://app.example/_next/static/chunks/gone.js", {
        headers: { rsc: "1" },
      }),
      deps,
    );

    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).not.toBe("text/x-component");
    expect(entries()).toEqual([]);
  });
});

describe("not-found fallback for unmatched pathnames", () => {
  it("renders the notFound error route's body with status 404 when the pathname has no dispatch entry", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/404"],
        routes: {},
        dispatch: {
          "/404": { kind: "lambda", id: "page", entryKey: "/404", page: true },
        },
        errorRoutes: { notFound: "/404" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("<html>custom 404</html>", {
          status: 200,
          headers: { "content-type": "text/html" },
        })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      {},
      new Request("https://app.example/never-registered"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<html>custom 404</html>");
  });

  it("falls back to the plaintext 404 when the manifest has no errorRoutes.notFound", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {},
      },
      assetStore: assetStoreServing({}),
    });

    const res = await dispatchResult(
      {},
      new Request("https://app.example/never-registered"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
  });

  it("serves a /_next/static asset from the store instead of the not-found page", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/404"],
        routes: {},
        dispatch: {
          "/404": { kind: "lambda", id: "page", entryKey: "/404", page: true },
        },
        errorRoutes: { notFound: "/404" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("<html>custom 404</html>", {
          status: 200,
          headers: { "content-type": "text/html" },
        })) as unknown as typeof fetch,
      assetStore: assetStoreServing({
        "/_next/static/chunks/app.js": "console.log(1)",
      }),
    });

    const res = await dispatchResult(
      {},
      new Request("https://app.example/_next/static/chunks/app.js"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("console.log(1)");
  });

  it("returns the plaintext 404 for a missing /_next/static asset instead of the not-found page", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/404"],
        routes: {},
        dispatch: {
          "/404": { kind: "lambda", id: "page", entryKey: "/404", page: true },
        },
        errorRoutes: { notFound: "/404" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("<html>custom 404</html>", {
          status: 200,
          headers: { "content-type": "text/html" },
        })) as unknown as typeof fetch,
      assetStore: assetStoreServing({}),
    });

    const res = await dispatchResult(
      {},
      new Request("https://app.example/_next/static/invalid-path"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
  });

  it("returns the plaintext 404 for a missing /_next/static asset under a basePath", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "/base",
        pathnames: ["/base/404"],
        routes: {},
        dispatch: {
          "/base/404": { kind: "lambda", id: "page", entryKey: "/base/404", page: true },
        },
        errorRoutes: { notFound: "/base/404" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("<html>custom 404</html>", {
          status: 200,
          headers: { "content-type": "text/html" },
        })) as unknown as typeof fetch,
      assetStore: assetStoreServing({}),
    });

    const res = await dispatchResult(
      {},
      new Request("https://app.example/base/_next/static/invalid-path"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
  });

  it("renders the not-found page for a genuinely unmatched pathname through the real router", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/404", "/dashboard"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [],
          onMatch: [],
          fallback: [],
        },
        dispatch: {
          "/404": { kind: "lambda", id: "page", entryKey: "/404", page: true },
          "/dashboard": { kind: "static" },
        },
        errorRoutes: { notFound: "/404" },
      },
      functionUrls: { page: "https://fn.example.com" },
      fetch: (async () =>
        new Response("<html>custom 404</html>", {
          status: 200,
          headers: { "content-type": "text/html" },
        })) as unknown as typeof fetch,
      assetStore: assetStoreServing({ "/dashboard.html": "<html>dashboard</html>" }),
    });

    const res = await serve(
      new Request("https://app.example/catch-all"),
      deps,
    );

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<html>custom 404</html>");
  });
});

describe("ruleDestinationPathname substitution with prefix-colliding groups", () => {
  it("does not let a numbered $1 eat into $10", () => {
    const sourceRegex =
      "^/(a)/(b)/(c)/(d)/(e)/(f)/(g)/(h)/(i)/(j)(?:/)?$";
    const out = ruleDestinationPathname(
      sourceRegex,
      "/$1-$10",
      "/a/b/c/d/e/f/g/h/i/j",
    );

    expect(out).toBe("/a-j");
  });

  it("does not let a named $id eat into $id2", () => {
    const sourceRegex = "^/(?<id>[^/]+?)/(?<id2>[^/]+?)(?:/)?$";
    const out = ruleDestinationPathname(sourceRegex, "/$id/$id2", "/a/b");

    expect(out).toBe("/a/b");
  });
});

describe("nested dynamic params with a prefix-colliding name", () => {
  it("resolves the second param from the path, not corrupted by the first's substitution", async () => {
    let invoked: URL | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/[id]/[id2]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [],
          dynamicRoutes: [
            {
              sourceRegex: "^/(?<nxtPid>[^/]+?)/(?<nxtPid2>[^/]+?)(?:/)?$",
              destination: "/[id]/[id2]?nxtPid=$nxtPid&nxtPid2=$nxtPid2",
            },
          ],
          onMatch: [],
          fallback: [],
        } as unknown as RouteDeps["manifest"]["routes"],
        dispatch: { "/[id]/[id2]": { kind: "lambda", id: "page" } },
      },
      functionUrls: { page: "https://page.example.com" },
      fetch: (async (req: Request) => {
        invoked = new URL(req.url);
        return new Response("ok", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await serve(new Request("https://app.example/a/b"), deps);

    expect(invoked?.searchParams.get("nxtPid")).toBe("a");
    expect(invoked?.searchParams.get("nxtPid2")).toBe("b");
  });
});
