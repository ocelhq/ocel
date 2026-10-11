import { baseDeps } from "@framework/next-router/test-support/dispatch-scenario";
import { describe, expect, it } from "vitest";
import { dispatchResult, type RouteDeps, serve } from "../src/index";
import { fakeServing } from "./serving-deps";

describe("dispatchResult", () => {
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
      serving: fakeServing().serving,
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
      serving: fakeServing().serving,
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

  it("forwards the RSC-family headers to Serve past allowHeader", async () => {
    const served = fakeServing();
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
      serving: served.serving,
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

    const sent = served.calls[0].request;
    expect(sent.headers.get("rsc")).toBe("1");
    expect(sent.headers.get("next-router-prefetch")).toBe("1");
    expect(sent.headers.get("next-router-state-tree")).toBe("%5B%22%22%5D");
    expect(new URL(sent.url).search).toBe("");
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
      serving: fakeServing().serving,
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
  });

  it("hands Serve none of the client's cookie for a cacheable prerender", async () => {
    const served = fakeServing();
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
      serving: served.serving,
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
    expect(served.calls[0].request.headers.get("cookie")).toBeNull();
  });
});

describe("an afterFiles rewrite shadowed by a dynamic route", () => {
  it("uses the exact page's own prerender config after the swap, not the shadowing template's", async () => {
    const served = fakeServing();
    const deps = baseDeps({
      manifest: {
        buildId: "t",
        basePath: "",
        pathnames: ["/ssg/hello", "/ssg/[slug]"],
        routes: {
          beforeMiddleware: [],
          beforeFiles: [],
          afterFiles: [{ sourceRegex: "^/to-ssg(?:/)?$", destination: "/ssg/hello" }],
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
      serving: served.serving,
    });

    const res = await serve(
      new Request("https://app.example/to-ssg", {
        headers: { "x-marker-hello": "1", "x-marker-slug": "1" },
      }),
      deps,
    );

    expect(res.headers.get("x-matched-path")).toBe("/ssg/hello");
    const sent = served.calls[0].request;
    expect(new URL(sent.url).host).toBe("hello.example.com");
    expect(sent.headers.get("x-marker-hello")).toBe("1");
    expect(sent.headers.has("x-marker-slug")).toBe(false);
  });
});

describe("an origin that cannot answer a segment prefetch", () => {
  function prefetch(originHeaders: Record<string, string>) {
    const served = fakeServing(
      () =>
        new Response("SHELL-OR-SEGMENT", {
          status: 200,
          headers: { "cf-cache-status": "MISS", ...originHeaders },
        }),
    );
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
      serving: served.serving,
    });
    return serve(
      new Request("https://app.example/settings", {
        headers: {
          RSC: "1",
          "next-router-prefetch": "1",
          "next-router-segment-prefetch": "/$d$team/$d$project/settings",
        },
      }),
      deps,
    );
  }

  it("answers a 204 miss instead of handing the client a postponed shell", async () => {
    const res = await prefetch({ "x-nextjs-postponed": "1" });

    expect(res.status).toBe(204);
    expect(await res.text()).toBe("");
  });

  it("serves a real segment payload as the origin rendered it", async () => {
    const res = await prefetch({ "x-nextjs-postponed": "2" });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("SHELL-OR-SEGMENT");
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
      serving: fakeServing(
        () => new Response("rendered", { headers: { "cf-cache-status": "MISS" } }),
      ).serving,
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
});
