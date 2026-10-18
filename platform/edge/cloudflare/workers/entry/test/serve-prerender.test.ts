import { describe, expect, it, vi } from "vitest";
import type { ServeFetch } from "../src/index";
import {
  buildEdgeUrl,
  RENDER_RELEASE_HEADER,
  readEdgeEntry,
  SERVE_NO_SHELL_HEADER,
  SERVE_REFUSED_HEADER,
  SERVE_UNSHARED_HEADER,
} from "../src/serve-key";
import {
  EMPTY_ROUTES,
  heldPurgeQueue,
  servedNextRelease,
  UNTOKENED_ISR_PREFIX,
} from "./next-release";
import { type FakeServing, fakeServing } from "./serving-deps";

type Target = Record<string, unknown>;

const actionTarget: Target = {
  config: { allowHeader: ["host"], bypassFor: [{ type: "header", key: "next-action" }] },
};

function recorder(answer: () => Response = () => new Response("from-origin")) {
  const requests: Request[] = [];
  const fetch = (async (request: Request) => {
    requests.push(request);
    return answer();
  }) as unknown as typeof fetch;
  return { requests, fetch };
}

function blogRelease(
  served: FakeServing,
  over: {
    target?: Target;
    origin?: ReturnType<typeof recorder>;
    queue?: ReturnType<typeof heldPurgeQueue>["queue"];
    pending?: Promise<unknown>[];
    isrPrefix?: string;
  } = {},
): ServeFetch {
  const origin = over.origin ?? recorder();
  return servedNextRelease({
    table: {
      dispatch: {
        "/blog": {
          kind: "prerender",
          id: "/blog",
          config: { allowHeader: ["host"], bypassToken: "TOKEN" },
          allowQuery: ["page"],
          ...over.target,
        } as never,
      },
    },
    record: {
      functionUrls: { "/blog": "https://fn.example.com" },
      ...(over.isrPrefix && { isrPrefix: over.isrPrefix }),
    },
    fetch: origin.fetch,
    serve: served.serving.serve,
    serving: {
      ...(over.queue && { queue: over.queue }),
      ...(over.pending && { waitUntil: (promise) => void over.pending?.push(promise) }),
    },
  });
}

const get = (path: string, serve: ServeFetch, init?: RequestInit) =>
  serve(new Request(`https://Shop.Example.com${path}`, init));

function settlesWithin(promise: Promise<unknown>, ms: number): Promise<boolean> {
  return Promise.race([
    promise.then(() => true),
    new Promise<boolean>((resolve) => setTimeout(() => resolve(false), ms)),
  ]);
}

function cached(status: string, extra: Record<string, string> = {}) {
  return () =>
    new Response("page", {
      headers: {
        "cf-cache-status": status,
        "cache-tag": "r0a1b2c3d,r0a1b2c3d|path:/blog",
        "cloudflare-cdn-cache-control": "max-age=60",
        "cache-control": "public, max-age=0, must-revalidate",
        ...extra,
      },
    });
}

describe("a prerender served through Serve", () => {
  it("fetches the origin URL through Serve keyed by the public host, the app and the release token", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served));

    expect(served.calls).toHaveLength(1);
    expect(served.calls[0].props).toEqual({ kind: "origin" });
    expect(served.calls[0].coordinates).toEqual({
      host: "shop.example.com",
      app: "web",
      release: "r0a1b2c3d",
      variant: "",
    });
    expect(served.calls[0].originUrl).toBe("https://fn.example.com/blog");
  });

  it("tells the origin the request is for the edge cache, so it shapes the response", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served));

    expect(served.calls[0].request.headers.get("x-ocel-edge-cache")).toBe("1");
  });

  it("keys a visitor's own x-ocel-edge-cache out of what the origin is sent on a bypass", async () => {
    const origin = recorder();

    await get("/blog", blogRelease(fakeServing(), { origin, target: actionTarget }), {
      method: "POST",
      body: "x",
      headers: { "next-action": "1", "x-ocel-edge-cache": "1" },
    });

    expect(origin.requests[0].headers.has("x-ocel-edge-cache")).toBe(false);
  });

  it("hands Serve neither the visitor's cookie nor their authorization", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served), {
      headers: { cookie: "session=1", authorization: "Bearer t" },
    });

    const sent = served.calls[0].request.headers;
    expect(sent.has("cookie")).toBe(false);
    expect(sent.has("authorization")).toBe(false);
  });

  it("tells Serve the release whose functions the origin must be one of", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served));

    expect(
      JSON.parse(served.calls[0].request.headers.get(RENDER_RELEASE_HEADER) ?? "null"),
    ).toMatchObject({ slug: "p1", isrPrefix: "prod/p1/web/r0a1b2c3d/isr", tags: [] });
  });

  it("names the entry the durable store may answer from, with the windows of the route", async () => {
    const served = fakeServing();
    const target = { fallback: { initialRevalidate: 30, initialExpiration: 300 } };

    await get("/blog", blogRelease(served, { target }));

    expect(
      JSON.parse(served.calls[0].request.headers.get(RENDER_RELEASE_HEADER) ?? "null").entry,
    ).toEqual({
      path: "/blog",
      fallbackPath: "/blog",
      revalidate: 30,
      expire: 300,
      pathDeterminesQuery: true,
    });
  });

  it("names no revalidate window for a route that never revalidates", async () => {
    const served = fakeServing();
    const target = { fallback: { initialRevalidate: false } };

    await get("/blog", blogRelease(served, { target }));

    expect(
      JSON.parse(served.calls[0].request.headers.get(RENDER_RELEASE_HEADER) ?? "null").entry,
    ).toEqual({ path: "/blog", fallbackPath: "/blog", pathDeterminesQuery: true });
  });

  it.each([
    ["a static route", [], "", true],
    ["a dynamic route", ["nxtPid"], "?nxtPid=a", true],
    ["a route whose every query varies the page", undefined, "?nxtPid=a&utm=x", false],
  ])(
    "keys %s by the query its allowQuery keeps, and says whether the path determines it",
    async (_, allowQuery, search, pathDeterminesQuery) => {
      const served = fakeServing();

      await get("/blog?utm=x&nxtPid=a", blogRelease(served, { target: { allowQuery } }));

      const sent = served.calls[0].request;
      expect(new URL(sent.url).search).toBe(search);
      const entry = JSON.parse(sent.headers.get(RENDER_RELEASE_HEADER) ?? "null").entry;
      expect(entry.pathDeterminesQuery === true).toBe(pathDeterminesQuery);
    },
  );

  it.each([
    ["document", {}, ".ppr-shell"],
    ["segment prefetch", { rsc: "1", "next-router-segment-prefetch": "/_tree" }, ".segments"],
  ])(
    "says the path determines the query of a PPR route's %s, whose shell allowQuery keeps no query",
    async (_, headers, variant) => {
      const served = fakeServing();
      const target = { config: { renderingMode: "PARTIALLY_STATIC" }, allowQuery: [] };

      await get("/blog?nxtPid=a", blogRelease(served, { target }), { headers });

      const sent = served.calls[0].request;
      expect(decodeURIComponent(sent.url)).toContain(variant);
      expect(new URL(sent.url).search).toBe("");
      const entry = JSON.parse(sent.headers.get(RENDER_RELEASE_HEADER) ?? "null").entry;
      expect(entry.pathDeterminesQuery).toBe(true);
    },
  );

  it("renders at the origin, uncached, when Serve refuses the call", async () => {
    const served = fakeServing(
      () =>
        new Response("refused", {
          status: 503,
          headers: { "cache-control": "private, no-store", "x-ocel-serve-refused": "1" },
        }),
    );
    const origin = recorder(() => new Response("from-origin"));

    const res = await get("/blog", blogRelease(served, { origin }), {
      headers: { cookie: "session=1" },
    });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("from-origin");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.requests).toHaveLength(1);
    expect(origin.requests[0].headers.has("x-ocel-edge-cache")).toBe(false);
  });

  it("keys two requests that differ only in a query the route ignores to one URL", async () => {
    const served = fakeServing();

    await get("/blog?ref=a&page=2", blogRelease(served));
    await get("/blog?page=2&utm=b", blogRelease(served));

    expect(served.calls.map(({ originUrl }) => originUrl)).toEqual([
      "https://fn.example.com/blog?page=2",
      "https://fn.example.com/blog?page=2",
    ]);
    expect(served.calls[0].request.url).toBe(served.calls[1].request.url);
  });

  it("keys an RSC request apart from the document by variant", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served));
    await get("/blog", blogRelease(served), { headers: { rsc: "1" } });

    expect(served.calls.map(({ coordinates }) => coordinates.variant)).toEqual(["", ".rsc"]);
    expect(served.calls[0].request.url).not.toBe(served.calls[1].request.url);
    expect(served.calls[0].props).toEqual(served.calls[1].props);
  });

  it.each([
    ["HIT", "HIT"],
    ["REVALIDATED", "HIT"],
    ["STALE", "STALE"],
    ["UPDATING", "STALE"],
    ["MISS", "MISS"],
    ["EXPIRED", "MISS"],
    ["DYNAMIC", "MISS"],
  ])("reports Workers Cache status %s as the %s tier", async (status, tier) => {
    const res = await get("/blog", blogRelease(fakeServing(cached(status))));

    expect(res.headers.get("x-ocel-cache")).toBe(tier);
  });

  it.each([
    ["store", "HIT"],
    ["stale-store", "STALE"],
  ])(
    "reports a miss Serve answered from the durable store (%s) as the %s tier, and keeps where it came from to itself",
    async (source, tier) => {
      const res = await get(
        "/blog",
        blogRelease(fakeServing(cached("MISS", { "x-ocel-entry-source": source }))),
      );

      expect(res.headers.get("x-ocel-cache")).toBe(tier);
      expect(res.headers.has("x-ocel-entry-source")).toBe(false);
    },
  );

  it("reports a hit by Workers Cache's status, whatever source filled it", async () => {
    const res = await get(
      "/blog",
      blogRelease(fakeServing(cached("UPDATING", { "x-ocel-entry-source": "store" }))),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("STALE");
  });

  it("reports a Serve response with no cache status as a miss", async () => {
    const res = await get("/blog", blogRelease(fakeServing(() => new Response("page"))));

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("keeps the cache's own headers off the visitor's response", async () => {
    const res = await get("/blog", blogRelease(fakeServing(cached("HIT"))));

    expect(res.headers.has("cache-tag")).toBe(false);
    expect(res.headers.has("cloudflare-cdn-cache-control")).toBe(false);
    expect(res.headers.has("cf-cache-status")).toBe(false);
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });

  it("gives the visitor back the Vary the origin sent, which Serve moved to x-ocel-vary", async () => {
    const res = await get(
      "/blog",
      blogRelease(fakeServing(cached("HIT", { "x-ocel-vary": "rsc, next-router-state-tree" }))),
    );

    expect(res.headers.get("vary")).toBe("rsc, next-router-state-tree");
    expect(res.headers.has("x-ocel-vary")).toBe(false);
  });

  it("reports a stored page's x-nextjs-cache as the tier that served it", async () => {
    const res = await get(
      "/blog",
      blogRelease(fakeServing(cached("HIT", { "x-nextjs-cache": "MISS" }))),
    );

    expect(res.headers.get("x-nextjs-cache")).toBe("HIT");
  });

  it("answers a HEAD from the entry a GET fills, with its headers and no body", async () => {
    const served = fakeServing(cached("HIT", { etag: '"e1"' }));
    const origin = recorder();

    const res = await get("/blog", blogRelease(served, { origin }), { method: "HEAD" });

    expect(origin.requests).toHaveLength(0);
    expect(served.calls).toHaveLength(1);
    expect(served.calls[0].request.method).toBe("GET");
    expect(res.status).toBe(200);
    expect(res.headers.get("etag")).toBe('"e1"');
    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await res.text()).toBe("");
  });

  it("sends a HEAD to the origin as a HEAD when Serve refuses the origin", async () => {
    const served = fakeServing(
      () => new Response("no", { status: 503, headers: { [SERVE_REFUSED_HEADER]: "1" } }),
    );
    const origin = recorder();

    await get("/blog", blogRelease(served, { origin }), { method: "HEAD" });

    expect(origin.requests).toHaveLength(1);
    expect(origin.requests[0].method).toBe("HEAD");
  });
});

describe("a prerender whose Serve call answers a redirect or fails", () => {
  for (const status of [307, 308]) {
    it(`returns the ${status} and its Location to the visitor`, async () => {
      const served = fakeServing(
        () => new Response(null, { status, headers: { location: "/elsewhere" } }),
      );

      const res = await get("/blog", blogRelease(served));

      expect(res.status).toBe(status);
      expect(res.headers.get("location")).toBe("/elsewhere");
    });
  }

  it("is forwarded to the origin uncached, and logged, when the Serve call throws", async () => {
    const served = fakeServing(() => {
      throw new Error("serve exploded");
    });
    const origin = recorder();
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    const res = await get("/blog", blogRelease(served, { origin }));

    expect(await res.text()).toBe("from-origin");
    expect(origin.requests).toHaveLength(1);
    expect(origin.requests[0].headers.has("x-ocel-edge-cache")).toBe(false);
    expect(logged).toHaveBeenCalled();
    logged.mockRestore();
  });
});

describe("a prerender that must not be stored", () => {
  async function throughOrigin(
    path: string,
    init: RequestInit | undefined,
    target?: Target,
  ): Promise<{ res: Response; served: FakeServing; origin: ReturnType<typeof recorder> }> {
    const served = fakeServing();
    const origin = recorder();
    const res = await get(path, blogRelease(served, { origin, target }), init);
    expect(served.calls).toHaveLength(0);
    return { res, served, origin };
  }

  it("goes to the origin for a draft-mode visitor, with their cookie", async () => {
    const { res, origin } = await throughOrigin("/blog", {
      headers: { cookie: "__prerender_bypass=abc" },
    });

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.requests[0].headers.get("cookie")).toBe("__prerender_bypass=abc");
  });

  it("goes to the origin for a POST", async () => {
    const { res } = await throughOrigin(
      "/blog",
      { method: "POST", body: "x", headers: { "next-action": "1" } },
      actionTarget,
    );

    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("goes to the origin for a runtime prefetch, which no cache may answer", async () => {
    const { res } = await throughOrigin("/blog", {
      headers: { rsc: "1", "next-router-prefetch": "2" },
    });

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("goes to the origin for bare RSC of a PPR route", async () => {
    const { res } = await throughOrigin(
      "/blog",
      { headers: { rsc: "1" } },
      {
        config: { renderingMode: "PARTIALLY_STATIC" },
      },
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });
});

describe("the document of a PPR route", () => {
  const pprTarget: Target = { config: { renderingMode: "PARTIALLY_STATIC" } };
  const state = "STATE-\u00e9";

  function framedShell(status = "HIT") {
    return () =>
      new Response(`${state}<html>shell`, {
        headers: {
          "cf-cache-status": status,
          "x-ocel-postponed-length": String(new TextEncoder().encode(state).byteLength),
          "x-nextjs-postponed": "1",
          "cache-control": "public, max-age=0, must-revalidate",
          "content-length": "99",
        },
      });
  }

  it("asks Serve for the route's static shell, keyed apart from its document and RSC", async () => {
    const served = fakeServing(framedShell());

    await get("/blog", blogRelease(served, { target: pprTarget }));

    expect(served.calls).toHaveLength(1);
    expect(served.calls[0].coordinates.variant).toBe(".ppr-shell");
    expect(served.calls[0].props).toEqual({ kind: "origin" });
    expect(served.calls[0].request.headers.get("x-ocel-edge-cache")).toBe("1");
    expect(served.calls[0].request.headers.has("cookie")).toBe(false);
  });

  it("serves the cached shell and resumes the dynamic part at the origin with its postponed state", async () => {
    const served = fakeServing(framedShell());
    const origin = recorder(() => new Response("<p>dynamic</p>"));

    const res = await get("/blog", blogRelease(served, { origin, target: pprTarget }), {
      headers: { cookie: "session=1" },
    });

    expect(await res.text()).toBe("<html>shell<p>dynamic</p>");
    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(res.headers.has("x-ocel-postponed-length")).toBe(false);
    expect(res.headers.has("content-length")).toBe(false);
    expect(origin.requests).toHaveLength(1);
    const resume = origin.requests[0];
    expect(resume.method).toBe("POST");
    expect(resume.headers.get("next-resume")).toBe("1");
    expect(resume.headers.get("cookie")).toBe("session=1");
    expect(new URL(resume.url).toString()).toBe("https://fn.example.com/blog");
    expect(await resume.text()).toBe(state);
  });

  it("answers a HEAD with the composed page's status and headers, and resumes nothing", async () => {
    const served = fakeServing(framedShell());
    const origin = recorder(() => new Response("<p>dynamic</p>"));

    const res = await get("/blog", blogRelease(served, { origin, target: pprTarget }), {
      method: "HEAD",
    });

    expect(served.calls).toHaveLength(1);
    expect(served.calls[0].request.method).toBe("GET");
    expect(origin.requests).toHaveLength(0);
    expect(res.status).toBe(200);
    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(res.headers.has("x-ocel-postponed-length")).toBe(false);
    expect(await res.text()).toBe("");
  });

  it("resumes with the route's own resume headers", async () => {
    const served = fakeServing(framedShell());
    const origin = recorder(() => new Response("<p>dynamic</p>"));

    await get(
      "/blog",
      blogRelease(served, {
        origin,
        target: { ...pprTarget, pprChain: { headers: { "next-resume": "1", "x-chain": "on" } } },
      }),
    );

    expect(origin.requests[0].headers.get("x-chain")).toBe("on");
  });

  it("serves the shell alone when the resume fails", async () => {
    const served = fakeServing(framedShell());
    const origin = recorder(() => new Response("boom", { status: 500 }));

    const res = await get("/blog", blogRelease(served, { origin, target: pprTarget }));

    expect(await res.text()).toBe("<html>shell");
  });

  it("renders the page at the origin when the shell does not carry the postponed state it declares", async () => {
    const served = fakeServing(() => {
      const shell = framedShell()();
      const headers = new Headers(shell.headers);
      headers.set("x-ocel-postponed-length", "4096");
      return new Response(`${state}<html>shell`, { headers });
    });
    const origin = recorder(() => new Response("<html>whole page"));

    const res = await get("/blog", blogRelease(served, { origin, target: pprTarget }), {
      headers: { cookie: "session=1" },
    });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>whole page");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.requests).toHaveLength(1);
    const rendered = origin.requests[0];
    expect(rendered.method).toBe("GET");
    expect(rendered.headers.get("cookie")).toBe("session=1");
    expect(rendered.headers.has("x-ocel-edge-cache")).toBe(false);
  });

  it("renders the page at the origin as one plain GET when Serve has no shell for it", async () => {
    const served = fakeServing(
      () =>
        new Response(null, {
          status: 404,
          headers: { "cache-control": "private, no-store", [SERVE_NO_SHELL_HEADER]: "1" },
        }),
    );
    const origin = recorder(() => new Response("<html>whole page"));

    const res = await get("/blog", blogRelease(served, { origin, target: pprTarget }), {
      headers: { cookie: "session=1" },
    });

    expect(await res.text()).toBe("<html>whole page");
    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
    expect(served.calls).toHaveLength(1);
    expect(origin.requests).toHaveLength(1);
    const rendered = origin.requests[0];
    expect(rendered.method).toBe("GET");
    expect(rendered.headers.get("cookie")).toBe("session=1");
    expect(rendered.headers.has("x-ocel-edge-cache")).toBe(false);
    expect(rendered.headers.has("next-resume")).toBe(false);
  });

  it("serves a shell that did not postpone as the complete page it is", async () => {
    const served = fakeServing(cached("HIT"));
    const origin = recorder();

    const res = await get("/blog", blogRelease(served, { origin, target: pprTarget }));

    expect(await res.text()).toBe("page");
    expect(res.headers.get("x-ocel-cache")).toBe("HIT");
    expect(origin.requests).toHaveLength(0);
  });
});

describe("the document of a PPR route whose param was not prerendered", () => {
  const fallbackTarget: Target = {
    config: { renderingMode: "PARTIALLY_STATIC", partialFallback: true },
    allowQuery: ["page"],
  };

  function paramRelease(served: FakeServing, over: { target?: Target } = {}): ServeFetch {
    return servedNextRelease({
      table: {
        pathnames: ["/blog/[id]"],
        routes: {
          ...EMPTY_ROUTES,
          dynamicRoutes: [
            {
              sourceRegex: "^/blog/(?<nxtPid>[^/]+?)(?:/)?$",
              destination: "/blog/[id]?nxtPid=$nxtPid",
            },
          ],
        },
        dispatch: {
          "/blog/[id]": { kind: "prerender", id: "/blog", ...over.target } as never,
        },
      },
      record: { functionUrls: { "/blog": "https://fn.example.com" } },
      fetch: recorder().fetch,
      serve: served.serving.serve,
    });
  }

  const getParam = (id: string, serve: ServeFetch) =>
    serve(new Request(`https://shop.example.com/blog/${id}?utm=x`));

  it("asks Serve for one shell shared by every param value", async () => {
    const served = fakeServing();

    await getParam("1", paramRelease(served, { target: fallbackTarget }));
    await getParam("2", paramRelease(served, { target: fallbackTarget }));

    expect(new URL(served.calls[0].request.url).pathname.endsWith("/blog/[id]")).toBe(true);
    expect(served.calls[1].request.url).toBe(served.calls[0].request.url);
  });

  it("marks the call as the shared shell, which Serve fills only from the store", async () => {
    const served = fakeServing();

    await getParam("42", paramRelease(served, { target: fallbackTarget }));

    const release = JSON.parse(
      served.calls[0].request.headers.get(RENDER_RELEASE_HEADER) ?? "null",
    );
    expect(release.sharesFallbackShell).toBe(true);
    expect(release).not.toHaveProperty("originPath");
  });

  it("keys the param by its own path when Serve answers that the shared shell is unavailable", async () => {
    const served = fakeServing((call) =>
      new URL(call.request.url).pathname.endsWith("/blog/[id]")
        ? new Response(null, { status: 404, headers: { [SERVE_UNSHARED_HEADER]: "1" } })
        : new Response("shell of 42", { headers: { "cf-cache-status": "MISS" } }),
    );

    const response = await getParam("42", paramRelease(served, { target: fallbackTarget }));

    expect(served.calls).toHaveLength(2);
    expect(new URL(served.calls[1].request.url).pathname.endsWith("/blog/42")).toBe(true);
    const release = JSON.parse(
      served.calls[1].request.headers.get(RENDER_RELEASE_HEADER) ?? "null",
    );
    expect(release).not.toHaveProperty("sharesFallbackShell");
    expect(release.entry).toEqual({
      path: "/blog/42",
      fallbackPath: "/blog/[id]",
      pathDeterminesQuery: true,
    });
    expect(await response.text()).toBe("shell of 42");
    expect(response.headers.has(SERVE_UNSHARED_HEADER)).toBe(false);
  });

  it("names the shared shell as the entry to answer from", async () => {
    const served = fakeServing();

    await getParam("42", paramRelease(served, { target: fallbackTarget }));

    expect(
      JSON.parse(served.calls[0].request.headers.get(RENDER_RELEASE_HEADER) ?? "null").entry,
    ).toEqual({ path: "/blog/[id]", fallbackPath: "/blog/[id]", pathDeterminesQuery: true });
  });

  it("keeps a shell per path when the route's fallback is not partial", async () => {
    const served = fakeServing();
    const target = { config: { renderingMode: "PARTIALLY_STATIC" } };

    await getParam("1", paramRelease(served, { target }));
    await getParam("2", paramRelease(served, { target }));

    expect(served.calls[1].request.url).not.toBe(served.calls[0].request.url);
  });
});

describe("an edge-runtime prerender", () => {
  const edgeTarget: Target = { edgeEntryKey: "middleware_app/edge-blog", tags: ["posts"] };

  it("is rendered by Serve and stored under the purge scope, keyed by its edge entry, host and release", async () => {
    const served = fakeServing();
    const origin = recorder();

    await get("/blog?page=2&utm=x", blogRelease(served, { origin, target: edgeTarget }));

    expect(origin.requests).toHaveLength(0);
    expect(served.calls).toHaveLength(1);
    const [call] = served.calls;
    expect(call.props).toEqual({ kind: "origin" });
    expect(call.coordinates).toEqual({
      host: "shop.example.com",
      app: "web",
      release: "r0a1b2c3d",
      variant: "",
    });
    expect(readEdgeEntry(call.request.url)).toBe("middleware_app/edge-blog");
    expect(buildEdgeUrl(call.request.url)).toBe("https://shop.example.com/blog?page=2");
  });

  it("tells Serve the release to render it from and the route's tags", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served, { target: edgeTarget }));

    expect(
      JSON.parse(served.calls[0].request.headers.get(RENDER_RELEASE_HEADER) ?? "null"),
    ).toEqual({
      slug: "p1",
      isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
      tags: ["posts"],
    });
  });

  it("keys its RSC variant apart from its document", async () => {
    const served = fakeServing();

    await get("/blog", blogRelease(served, { target: edgeTarget }));
    await get("/blog", blogRelease(served, { target: edgeTarget }), { headers: { rsc: "1" } });

    expect(served.calls.map(({ coordinates }) => coordinates.variant)).toEqual(["", ".rsc"]);
    expect(served.calls[0].request.url).not.toBe(served.calls[1].request.url);
  });
});

describe("a prerender of a release whose ISR prefix names no release token", () => {
  it("renders at the origin and stores nothing", async () => {
    const origin = recorder();
    const served = fakeServing();

    const res = await get(
      "/blog",
      blogRelease(served, { origin, isrPrefix: UNTOKENED_ISR_PREFIX }),
    );

    expect(served.calls).toHaveLength(0);
    expect(await res.text()).toBe("from-origin");
    expect(origin.requests).toHaveLength(1);
  });
});

describe("an on-demand revalidation with the bypass token", () => {
  const onDemand = { headers: { "x-prerender-revalidate": "TOKEN" } };

  it("renders at the origin with the token, past the cache", async () => {
    const served = fakeServing();
    const origin = recorder();

    const res = await get("/blog", blogRelease(served, { origin }), onDemand);

    expect(served.calls).toHaveLength(0);
    expect(origin.requests[0].headers.get("x-prerender-revalidate")).toBe("TOKEN");
    expect(res.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("then purges the path's tag in the release, and waits for it", async () => {
    const held = heldPurgeQueue();

    const answered = get("/blog", blogRelease(fakeServing(), { queue: held.queue }), onDemand);

    expect(await settlesWithin(answered, 50)).toBe(false);
    expect(held.raised).toEqual([{ mode: "purge", tags: ["r0a1b2c3d|path:/blog"] }]);
    held.release();
    await answered;
  });

  it("purges nothing when the render failed", async () => {
    const held = heldPurgeQueue();
    held.release();
    const pending: Promise<unknown>[] = [];
    const origin = recorder(() => new Response("boom", { status: 500 }));

    await get(
      "/blog",
      blogRelease(fakeServing(), { origin, queue: held.queue, pending }),
      onDemand,
    );
    await Promise.all(pending);

    expect(held.raised).toEqual([]);
  });

  it("does not run for a visitor who sent no token", async () => {
    const held = heldPurgeQueue();
    held.release();
    const pending: Promise<unknown>[] = [];

    await get("/blog", blogRelease(fakeServing(), { queue: held.queue, pending }), {
      headers: { "x-prerender-revalidate": "WRONG" },
    });
    await Promise.all(pending);

    expect(held.raised).toEqual([]);
  });
});

describe("an origin response announcing a purge", () => {
  function acting(
    headers: Record<string, string>,
    held: ReturnType<typeof heldPurgeQueue>,
    pending: Promise<unknown>[] = [],
  ) {
    const origin = recorder(
      () =>
        new Response("done", {
          headers: { "x-ocel-purge": "invalidate r0a1b2c3d|a purge r0a1b2c3d|b", ...headers },
        }),
    );
    return blogRelease(fakeServing(), {
      origin,
      target: actionTarget,
      queue: held.queue,
      pending,
    });
  }

  const post = (serve: ServeFetch) =>
    get("/blog", serve, { method: "POST", body: "x", headers: { "next-action": "1" } });

  it("hands the gateway's serving the groups, unawaited for a route handler", async () => {
    const held = heldPurgeQueue();
    const pending: Promise<unknown>[] = [];
    const answered = post(acting({}, held, pending));

    expect(await settlesWithin(answered, 1_000)).toBe(true);
    held.release();
    await Promise.all(pending);

    expect(held.raised).toEqual([
      { mode: "invalidate", tags: ["r0a1b2c3d|a"] },
      { mode: "purge", tags: ["r0a1b2c3d|b"] },
    ]);
    expect((await answered).headers.has("x-ocel-purge")).toBe(false);
  });

  it("has the gateway await the purge for a Server Action that revalidated", async () => {
    const held = heldPurgeQueue();

    const answered = post(acting({ "x-action-revalidated": "[[],0,0]" }, held));

    expect(await settlesWithin(answered, 50)).toBe(false);
    held.release();
    await answered;
  });
});
