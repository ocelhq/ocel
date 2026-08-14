import { describe, expect, it } from "vitest";

import { dispatchResult, type RouteDeps } from "../src/index";
import { coloDeps } from "./cache-deps";

const target = {
  kind: "prerender" as const,
  id: "/blog",
  config: { allowHeader: ["host"], bypassToken: "TOKEN" },
  fallback: { initialRevalidate: 60 },
};

function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

function origin(body: () => string) {
  const requests: Request[] = [];
  const fetch = (async (request: Request) => {
    requests.push(request);
    return new Response(body(), {
      status: 200,
      headers: {
        "cache-control": "s-maxage=60",
        "x-nextjs-cache": "REVALIDATED",
      },
    });
  }) as unknown as typeof fetch;
  return { requests, fetch };
}

function colo() {
  const entries = new Map<string, Response>();
  const pending: Promise<unknown>[] = [];
  return {
    entries,
    settle: () => Promise.all(pending.splice(0)),
    deps: coloDeps({
      cache: {
        match: async (request: Request) => entries.get(request.url)?.clone(),
        put: async (request: Request, response: Response) => {
          entries.set(request.url, response);
        },
      } as unknown as Cache,
      waitUntil: (promise: Promise<unknown>) => {
        pending.push(promise);
      },
    }),
  };
}

function deps(served: ReturnType<typeof origin>, store: ReturnType<typeof colo>): RouteDeps {
  return {
    manifest: {
      buildId: "t",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: { "/blog": target },
    },
    functionUrls: { "/blog": "https://fn.example.com" },
    slug: "p1",
    app: "web",
    assetStore: noAssets(),
    fetch: served.fetch,
    cache: store.deps,
  };
}

const dispatch = (routeDeps: RouteDeps, request: Request) =>
  dispatchResult(
    { resolvedPathname: "/blog", invocationTarget: { pathname: "/blog" } },
    request,
    routeDeps,
  );

const revalidate = (method: string) =>
  new Request("https://app.example/blog", {
    method,
    headers: { "x-prerender-revalidate": "TOKEN" },
  });

describe("an on-demand revalidation arriving at the edge", () => {
  it("answers res.revalidate's HEAD probe with a bodiless 200", async () => {
    const served = origin(() => "<html>fresh</html>");
    const store = colo();

    const res = await dispatch(deps(served, store), revalidate("HEAD"));

    expect(res.status).toBe(200);
    expect(res.body).toBeNull();
    expect(res.headers.get("x-nextjs-cache")).toBe("REVALIDATED");
  });

  it("renders the route as a GET, so there is a body to store", async () => {
    const served = origin(() => "<html>fresh</html>");
    const store = colo();

    await dispatch(deps(served, store), revalidate("HEAD"));

    expect(served.requests).toHaveLength(1);
    expect(served.requests[0]!.method).toBe("GET");
    expect(served.requests[0]!.headers.get("x-prerender-revalidate")).toBe("TOKEN");
  });

  it("stores the on-demand render, so the next request serves it", async () => {
    let body = "<html>first</html>";
    const served = origin(() => body);
    const store = colo();
    const routeDeps = deps(served, store);

    await dispatch(routeDeps, new Request("https://app.example/blog"));
    await store.settle();

    body = "<html>second</html>";
    await dispatch(routeDeps, revalidate("HEAD"));

    const after = await dispatch(routeDeps, new Request("https://app.example/blog"));
    expect(after.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await after.text()).toBe("<html>second</html>");
  });

  it("answers a GET revalidation with the render it just stored", async () => {
    const served = origin(() => "<html>fresh</html>");
    const store = colo();

    const res = await dispatch(deps(served, store), revalidate("GET"));

    expect(await res.text()).toBe("<html>fresh</html>");
    expect(store.entries.size).toBe(1);
  });

  it("leaves a request carrying the wrong token a plain miss", async () => {
    const served = origin(() => "<html>fresh</html>");
    const store = colo();

    const res = await dispatch(
      deps(served, store),
      new Request("https://app.example/blog", {
        headers: { "x-prerender-revalidate": "WRONG" },
      }),
    );

    expect(res.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("still refuses a POST to the prerendered document", async () => {
    const served = origin(() => "<html>fresh</html>");
    const store = colo();

    const res = await dispatch(deps(served, store), revalidate("POST"));

    expect(res.status).toBe(405);
    expect(served.requests).toEqual([]);
  });
});
