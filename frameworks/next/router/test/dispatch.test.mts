import type { Route } from "@next/routing";
import { describe, expect, it } from "vitest";

import { dispatchResult, ruleDestinationPathname, serve, type RouteDeps } from "../src/index.mjs";
import { assetStoreServing, baseDeps } from "../test-support/dispatch-scenario.mjs";

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

  it("strips cache-tag from a response the front does not invalidate by", async () => {
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
          headers: { "cache-tag": "r0a1b2c3d|products", "x-custom": "kept" },
        })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/tags", invocationTarget: { pathname: "/tags" } },
      new Request("https://app.example/tags"),
      deps,
    );

    expect(res.headers.get("cache-tag")).toBeNull();
    expect(res.headers.get("x-custom")).toBe("kept");
  });

  it("carries cache-tag out to a front that invalidates by it", async () => {
    const deps = baseDeps({
      keepCacheTags: true,
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
          headers: {
            "cache-tag": "r0a1b2c3d|products",
            "x-next-cache-tags": "products",
          },
        })) as unknown as typeof fetch,
    });

    const res = await dispatchResult(
      { resolvedPathname: "/tags", invocationTarget: { pathname: "/tags" } },
      new Request("https://app.example/tags"),
      deps,
    );

    expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|products");
    expect(res.headers.get("x-next-cache-tags")).toBeNull();
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

  it("percent-encodes the second question mark Next writes into an icon URL", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/favicon.ico": { kind: "lambda", id: "fn" } },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("from-lambda", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      {
        resolvedPathname: "/favicon.ico",
        invocationTarget: { pathname: "/favicon.ico" },
      },
      new Request("https://app.example/favicon.ico?favicon.abc.ico?dpl=123"),
      deps,
    );

    const forwarded = new URL(captured!.url);
    expect(forwarded.pathname).toBe("/favicon.ico");
    expect(forwarded.search).toBe("?favicon.abc.ico%3Fdpl=123");
  });

  it("leaves an ordinary query untouched on its way to the Function URL", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/search": { kind: "lambda", id: "fn" } },
      },
      functionUrls: { fn: "https://fn.example.com" },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("from-lambda", { status: 200 });
      }) as unknown as typeof fetch,
    });

    await dispatchResult(
      { resolvedPathname: "/search", invocationTarget: { pathname: "/search" } },
      new Request("https://app.example/search?a=1&b=2"),
      deps,
    );

    expect(new URL(captured!.url).search).toBe("?a=1&b=2");
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

  it("drops the control headers a client sends and forwards every other x-ocel-* header", async () => {
    let captured: Request | undefined;
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: {
          "/api/documents": {
            kind: "lambda",
            id: "/api/documents",
            entryKey: "/api/documents",
          },
        },
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
        headers: {
          "x-ocel-entry": "/admin",
          "next-resume": "1",
          "x-ocel-probe": "probe-value",
        },
      }),
      deps,
    );

    expect(captured?.headers.get("x-ocel-entry")).toBe("/api/documents");
    expect(captured?.headers.get("next-resume")).toBeNull();
    expect(captured?.headers.get("x-ocel-probe")).toBe("probe-value");
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
