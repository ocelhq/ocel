import { describe, expect, it } from "vitest";

import { dispatchResult, resolveRsc, type RouteDeps } from "../src/index";

// A Fetcher-like stub returning a fixed status/body for every request.
function assetsReturning(status: number, body = ""): RouteDeps["assets"] {
  return {
    fetch: async () => new Response(body, { status }),
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
    assets: assetsReturning(404),
    ...overrides,
  };
}

// Verbatim from examples/next-test/.ocel/output/routing-manifest.json, which is
// a build artifact and so cannot be imported here.
const rscConfig = {
  header: "rsc",
  suffix: ".rsc",
  prefetchSegmentHeader: "next-router-segment-prefetch",
  prefetchSegmentSuffix: ".segment.rsc",
  prefetchSegmentDirSuffix: ".segments",
};

// The RSC-bearing dispatch entries the real manifest emits for '/'.
function rscManifest(): RouteDeps["manifest"] {
  return {
    buildId: "t",
    basePath: "",
    pathnames: [],
    routes: { rsc: rscConfig },
    dispatch: {
      "/": { kind: "prerender", id: "/" },
      "/index.rsc": { kind: "prerender", id: "/" },
      "/index.segments/_tree.segment.rsc": { kind: "prerender", id: "/" },
      "/index.segments/__PAGE__.segment.rsc": { kind: "prerender", id: "/" },
      "/api/documents": { kind: "lambda", id: "/api/documents" },
      "/api/documents.rsc": { kind: "lambda", id: "/api/documents" },
    },
  };
}

function rscDeps(capture: (req: Request) => void): RouteDeps {
  return baseDeps({
    manifest: rscManifest(),
    functionUrls: {
      "/": "https://fn.example.com",
      "/api/documents": "https://fn.example.com",
    },
    fetch: (async (req: Request) => {
      capture(req);
      return new Response("from-origin", { status: 200 });
    }) as unknown as typeof fetch,
  });
}

describe("dispatchResult", () => {
  it("serves a static route from the Assets binding", async () => {
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: [],
        routes: {},
        dispatch: { "/next.svg": { kind: "static" } },
      },
      assets: assetsReturning(200, "<svg/>"),
    });

    const res = await dispatchResult(
      { resolvedPathname: "/next.svg" },
      new Request("https://app.example/next.svg"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
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

  it("falls back to the Assets binding when the path is not in the manifest", async () => {
    const deps = baseDeps({ assets: assetsReturning(200, "found") });

    const res = await dispatchResult(
      { resolvedPathname: "/unenumerated.txt" },
      new Request("https://app.example/unenumerated.txt"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("found");
  });

  it("returns 404 when neither the manifest nor the Assets binding has the path", async () => {
    const deps = baseDeps({ assets: assetsReturning(404) });

    const res = await dispatchResult(
      { resolvedPathname: "/missing" },
      new Request("https://app.example/missing"),
      deps,
    );

    expect(res.status).toBe(404);
  });

  it("falls back to Assets when routing produced no resolved pathname", async () => {
    const deps = baseDeps({ assets: assetsReturning(200, "asset") });

    const res = await dispatchResult(
      { resolvedPathname: null },
      new Request("https://app.example/whatever"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("asset");
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

  it("dispatches an RSC request for '/' via /index.rsc, forwarding the original path and headers", async () => {
    let captured: Request | undefined;
    const deps = rscDeps((req) => (captured = req));

    const res = await dispatchResult(
      { resolvedPathname: "/", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/", { headers: { rsc: "1" } }),
      deps,
    );

    expect(res.status).toBe(200);
    // The .rsc path is a dispatch identity only — Next in the Lambda has no
    // route by that name.
    expect(captured?.url).toBe("https://fn.example.com/");
    expect(captured?.headers.get("rsc")).toBe("1");
  });

  it("dispatches a segment prefetch via its .segments path, forwarding the original path and headers", async () => {
    let captured: Request | undefined;
    const deps = rscDeps((req) => (captured = req));

    const res = await dispatchResult(
      { resolvedPathname: "/", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/", {
        headers: { rsc: "1", "next-router-segment-prefetch": "/_tree" },
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(captured?.url).toBe("https://fn.example.com/");
    expect(captured?.headers.get("next-router-segment-prefetch")).toBe("/_tree");
  });

  it("dispatches a document request for '/' via '/', not /index.rsc", async () => {
    let captured: Request | undefined;
    const deps = rscDeps((req) => (captured = req));

    await dispatchResult(
      { resolvedPathname: "/", invocationTarget: { pathname: "/" } },
      new Request("https://app.example/"),
      deps,
    );

    expect(captured?.url).toBe("https://fn.example.com/");
    expect(captured?.headers.has("rsc")).toBe(false);
  });
});

describe("resolveRsc", () => {
  const manifest = rscManifest();

  it("leaves a request without RSC headers on the base pathname", () => {
    expect(resolveRsc("/", new Headers(), manifest)).toEqual({
      pathname: "/",
      cacheEligible: true,
    });
  });

  it("normalizes root to index before appending the suffix", () => {
    expect(resolveRsc("/", new Headers({ rsc: "1" }), manifest)).toEqual({
      pathname: "/index.rsc",
      cacheEligible: true,
    });
  });

  it("appends the suffix to a non-root pathname", () => {
    expect(
      resolveRsc("/api/documents", new Headers({ rsc: "1" }), manifest),
    ).toEqual({ pathname: "/api/documents.rsc", cacheEligible: true });
  });

  it("resolves a segment prefetch to its segment file", () => {
    const headers = new Headers({
      rsc: "1",
      "next-router-segment-prefetch": "/__PAGE__",
    });

    expect(resolveRsc("/", headers, manifest)).toEqual({
      pathname: "/index.segments/__PAGE__.segment.rsc",
      cacheEligible: true,
    });
  });

  it("falls back to the base pathname and flags cache-ineligible when the RSC path is not in dispatch", () => {
    expect(resolveRsc("/blog/hello", new Headers({ rsc: "1" }), manifest)).toEqual(
      { pathname: "/blog/hello", cacheEligible: false },
    );
  });

  it("flags cache-ineligible when a segment prefetch has no segment file", () => {
    const headers = new Headers({
      rsc: "1",
      "next-router-segment-prefetch": "/_missing",
    });

    expect(resolveRsc("/", headers, manifest)).toEqual({
      pathname: "/",
      cacheEligible: false,
    });
  });

  it("leaves the pathname alone when the manifest has no rsc block", () => {
    const noRsc = { ...rscManifest(), routes: {} };

    expect(resolveRsc("/", new Headers({ rsc: "1" }), noRsc)).toEqual({
      pathname: "/",
      cacheEligible: true,
    });
  });
});
