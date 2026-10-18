import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { describe, expect, it } from "vitest";
import { EMPTY_ROUTES, servedNextRelease, UNTOKENED_ISR_PREFIX } from "./next-release";
import { fakeServing } from "./serving-deps";

type Dispatch = NextRouteTable["dispatch"];

describe("a routed prerender", () => {
  it("invokes the parent function for a prerender route until ISR lands", async () => {
    const serve = servedNextRelease({
      table: { dispatch: { "/": { kind: "prerender", id: "/" } } },
      record: { functionUrls: { "/": "https://fn.example.com" }, isrPrefix: UNTOKENED_ISR_PREFIX },
      fetch: (async () => new Response("rendered", { status: 200 })) as unknown as typeof fetch,
    });

    const res = await serve(new Request("https://app.example/"));

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("rendered");
  });

  function bypassRelease(bypassKey: string) {
    return servedNextRelease({
      table: {
        dispatch: {
          "/preview": {
            kind: "prerender",
            id: "/preview",
            config: { bypassFor: [{ type: "cookie", key: bypassKey }] },
          },
        },
      },
      record: { functionUrls: { "/preview": "https://fn.example.com" } },
      fetch: (async () =>
        new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        })) as unknown as typeof fetch,
    });
  }

  const preview = (cookie: string) =>
    new Request("https://app.example/preview", { headers: { cookie } });

  it("does not treat a valueless cookie as a bypass match on a key prefix", async () => {
    const res = await bypassRelease("badcooki")(preview("badcookie"));
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("bypasses the cache when a real bypass cookie is present", async () => {
    const res = await bypassRelease("preview")(preview("preview=1"));
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  function draftRelease(capture: (req: Request) => void) {
    return servedNextRelease({
      table: {
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
      record: { functionUrls: { "/ssg-draft-mode/[[...route]]": "https://fn.example.com" } },
      fetch: (async (req: Request) => {
        capture(req);
        return new Response("rendered", { status: 200 });
      }) as unknown as typeof fetch,
    });
  }

  it("forwards the draft cookie to a prerender origin, which allowHeader omits", async () => {
    let captured: Request | undefined;
    const res = await draftRelease((req) => (captured = req))(
      new Request("https://app.example/ssg-draft-mode/test-1", {
        headers: { cookie: "__prerender_bypass=abc123" },
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(captured?.headers.get("cookie")).toBe("__prerender_bypass=abc123");
  });

  it("forwards a server action to a prerender origin with its own headers", async () => {
    let captured: Request | undefined;
    const res = await draftRelease((req) => (captured = req))(
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
    await draftRelease((req) => (captured = req))(
      new Request("https://app.example/ssg-draft-mode/test-1", {
        method: "POST",
        headers: { "next-resume": "1", "next-action": "abc" },
        body: "[1,{}]",
      }),
    );

    expect(captured?.headers.get("next-resume")).toBeNull();
  });

  const blog: Dispatch = {
    "/blog": { kind: "prerender", id: "/blog", config: { allowHeader: ["host"] } },
  };

  it("forwards the RSC-family headers to Serve past allowHeader", async () => {
    const served = fakeServing();
    const serve = servedNextRelease({
      table: { dispatch: blog },
      record: { functionUrls: { "/blog": "https://fn.example.com" } },
      serve: served.serving.serve,
    });

    await serve(
      new Request("https://app.example/blog?_rsc=abc", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "1",
          "next-router-state-tree": "%5B%22%22%5D",
        },
      }),
    );

    const sent = served.calls[0].request;
    expect(sent.headers.get("rsc")).toBe("1");
    expect(sent.headers.get("next-router-prefetch")).toBe("1");
    expect(sent.headers.get("next-router-state-tree")).toBe("%5B%22%22%5D");
    expect(new URL(sent.url).search).toBe("");
  });

  it("forwards the client's cookie to an uncacheable prerender origin", async () => {
    let captured: Request | undefined;
    const serve = servedNextRelease({
      table: { dispatch: blog },
      record: { functionUrls: { "/blog": "https://fn.example.com" } },
      fetch: (async (req: Request) => {
        captured = req;
        return new Response("rendered", {
          status: 200,
          headers: { "cache-control": "s-maxage=60" },
        });
      }) as unknown as typeof fetch,
    });

    const res = await serve(
      new Request("https://app.example/blog", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "2",
          cookie: "testCookie=initialValue",
        },
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(captured?.headers.get("cookie")).toBe("testCookie=initialValue");
  });

  it("hands Serve none of the client's cookie for a cacheable prerender", async () => {
    const served = fakeServing();
    const serve = servedNextRelease({
      table: { dispatch: blog },
      record: { functionUrls: { "/blog": "https://fn.example.com" } },
      serve: served.serving.serve,
    });

    const res = await serve(
      new Request("https://app.example/blog", {
        headers: {
          rsc: "1",
          "next-router-prefetch": "1",
          cookie: "testCookie=initialValue",
        },
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(served.calls[0].request.headers.get("cookie")).toBeNull();
  });
});

describe("an afterFiles rewrite shadowed by a dynamic route", () => {
  it("uses the exact page's own prerender config after the swap, not the shadowing template's", async () => {
    const served = fakeServing();
    const serve = servedNextRelease({
      table: {
        pathnames: ["/ssg/hello", "/ssg/[slug]"],
        routes: {
          ...EMPTY_ROUTES,
          afterFiles: [{ sourceRegex: "^/to-ssg(?:/)?$", destination: "/ssg/hello" }],
          dynamicRoutes: [
            {
              sourceRegex: "^/ssg/(?<nxtPslug>[^/]+?)(?:/)?$",
              destination: "/ssg/[slug]?nxtPslug=$nxtPslug",
            },
          ],
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
      record: {
        functionUrls: {
          "hello-fn": "https://hello.example.com",
          "slug-fn": "https://slug.example.com",
        },
      },
      serve: served.serving.serve,
    });

    const res = await serve(
      new Request("https://app.example/to-ssg", {
        headers: { "x-marker-hello": "1", "x-marker-slug": "1" },
      }),
    );

    expect(res.headers.get("x-matched-path")).toBe("/ssg/hello");
    const sent = served.calls[0].request;
    expect(new URL(served.calls[0].originUrl).origin).toBe("https://hello.example.com");
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
    const serve = servedNextRelease({
      table: {
        dispatch: {
          "/settings": {
            kind: "prerender",
            id: "settings",
            config: { renderingMode: "PARTIALLY_STATIC" },
            fallback: { initialRevalidate: 60 },
          },
        },
      },
      record: { functionUrls: { settings: "https://fn.example.com" } },
      serve: served.serving.serve,
    });
    return serve(
      new Request("https://app.example/settings", {
        headers: {
          RSC: "1",
          "next-router-prefetch": "1",
          "next-router-segment-prefetch": "/$d$team/$d$project/settings",
        },
      }),
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
