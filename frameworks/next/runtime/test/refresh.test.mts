import http from "node:http";
import { refreshHeader } from "@framework/next-cache";
import { afterEach, expect, test } from "vitest";
import { prerenderedRoutes } from "../src/prerendered-routes.mjs";
import {
  noteStaleEntry,
  type Refresh,
  readServedRoute,
  routeStaleHitsToRefresh,
} from "../src/refresh.mjs";

const prerender = {
  routes: {
    "/blog": { initialRevalidateSeconds: 60 },
    "/shop": { initialRevalidateSeconds: 60, renderingMode: "PARTIALLY_STATIC" },
    "/catalog": {
      initialRevalidateSeconds: 60,
      renderingMode: "PARTIALLY_STATIC",
      prefetchDataRoute: "/catalog.prefetch.rsc",
    },
    "/about": { initialRevalidateSeconds: false },
  },
  dynamicRoutes: {
    "/posts/[slug]": { routeRegex: "^/posts/([^/]+?)(?:/)?$", fallbackRevalidate: 60 },
  },
};

const routes = prerenderedRoutes({
  config: { cacheComponents: true },
  distDir: ".next",
  prerender,
});

const routesWithoutCacheComponents = prerenderedRoutes({
  config: {},
  distDir: ".next",
  prerender,
});

let server: http.Server | undefined;

afterEach(async () => {
  await new Promise<void>((resolve) => (server ? server.close(() => resolve()) : resolve()));
  server = undefined;
});

interface Seen {
  purpose: string | undefined;
  served: ReturnType<typeof readServedRoute>;
  scheduled: Refresh[];
  waited: Promise<unknown>[];
  status: number | undefined;
  body: string;
}

async function serving(
  render: (req: http.IncomingMessage, res: http.ServerResponse) => void,
  headers: Record<string, string> = {},
  path = "/blog?page=2",
  method = "GET",
  schedule?: (refresh: Refresh) => Promise<void>,
  hostRoutes = routes,
): Promise<Seen> {
  const seen: Seen = {
    purpose: undefined,
    served: undefined,
    scheduled: [],
    waited: [],
    status: undefined,
    body: "",
  };
  server = http.createServer((req, res) => {
    routeStaleHitsToRefresh(
      req,
      res,
      hostRoutes,
      schedule ??
        (async (refresh) => {
          seen.scheduled.push(refresh);
        }),
      (promise) => seen.waited.push(promise),
    );
    seen.purpose = req.headers.purpose as string | undefined;
    seen.served = readServedRoute(req.headers as Record<string | symbol, any>);
    render(req, res);
  });
  await new Promise<void>((resolve) => server!.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const { port } = server.address() as { port: number };
  await new Promise<void>((resolve, reject) => {
    const req = http.request(
      { host: "127.0.0.1", port, path, method, headers: { host: "shop.example", ...headers } },
      (res) => {
        seen.status = res.statusCode;
        res.setEncoding("utf8");
        res.on("data", (chunk) => {
          seen.body += chunk;
        });
        res.on("end", () => resolve());
      },
    );
    req.on("error", reject);
    req.end();
  });
  await Promise.all(seen.waited);
  return seen;
}

function staleHit(lastModified: number, key = "blog") {
  return (req: http.IncomingMessage, res: http.ServerResponse) => {
    noteStaleEntry(req.headers as Record<string | symbol, any>, { key, lastModified });
    res.end("stale page");
  };
}

test("asks Next to serve a stale hit as it is rather than re-render it in the background", async () => {
  const seen = await serving(staleHit(1_000));

  expect(seen.purpose).toBe("prefetch");
});

test("schedules a refresh of the stale entry it served, through the host", async () => {
  const seen = await serving(staleHit(1_000));

  expect(seen.scheduled).toEqual([
    {
      url: "/blog?page=2",
      key: "blog",
      lastModified: 1_000,
      headers: { host: "shop.example", [refreshHeader]: "1000" },
    },
  ]);
});

test("a stale hit schedules a refresh naming the entry it served", async () => {
  const seen = await serving(staleHit(1_000, "blog/page-2"));

  expect(seen.scheduled[0]?.key).toBe("blog/page-2");
});

test("a request that read two stale entries refreshes the newer one", async () => {
  const seen = await serving((req, res) => {
    const headers = req.headers as Record<string | symbol, any>;
    noteStaleEntry(headers, { key: "a", lastModified: 2_000 });
    noteStaleEntry(headers, { key: "b", lastModified: 1_000 });
    res.end("stale page");
  });

  expect(seen.scheduled).toHaveLength(1);
  expect(seen.scheduled[0]).toMatchObject({
    key: "a",
    lastModified: 2_000,
    headers: { [refreshHeader]: "2000" },
  });
});

test("a stale-entry note of another shape schedules nothing", async () => {
  const seen = await serving((req, res) => {
    (req.headers as Record<string | symbol, any>)[Symbol.for("ocel.next.stale-entry.v2")] = 1000;
    res.end("stale page");
  });

  expect(seen.scheduled).toEqual([]);
});

test("serves the whole stale page when the host's refresh throws as it is asked for", async () => {
  const seen = await serving(staleHit(1_000), {}, "/blog?page=2", "GET", () => {
    throw new Error("no slot");
  });

  expect(seen.status).toBe(200);
  expect(seen.body).toBe("stale page");
  expect(seen.waited).toHaveLength(1);
  await expect(seen.waited[0]).resolves.toBeUndefined();
});

test("refreshes the page, not the flight data, a stale RSC hit was served from", async () => {
  const seen = await serving(staleHit(1_000), { rsc: "1" }, "/blog?_rsc=abc&page=2");

  expect(seen.scheduled[0]?.url).toBe("/blog?page=2");
});

test("schedules nothing for a fresh hit", async () => {
  const seen = await serving((_req, res) => {
    res.setHeader("x-nextjs-cache", "HIT");
    res.end("fresh page");
  });

  expect(seen.scheduled).toEqual([]);
});

test("lets a refresh request re-render, and schedules nothing from it", async () => {
  const seen = await serving(staleHit(1_000), { [refreshHeader]: "1000" });

  expect(seen.purpose).toBeUndefined();
  expect(seen.scheduled).toEqual([]);
});

test("asks Next to serve a stale hit of a dynamic route that revalidates as it is", async () => {
  const seen = await serving(staleHit(1_000), {}, "/posts/hello");

  expect(seen.purpose).toBe("prefetch");
});

test("leaves a request to a route Next did not prerender as Next would see it", async () => {
  const seen = await serving((_req, res) => res.end("dynamic page"), {}, "/dashboard");

  expect(seen.purpose).toBeUndefined();
});

test("leaves a server action posted to a revalidating route as Next would see it", async () => {
  const seen = await serving((_req, res) => res.end("action result"), {}, "/blog", "POST");

  expect(seen.purpose).toBeUndefined();
});

test("schedules a refresh of a stale entry Next serves without saying it is stale", async () => {
  const seen = await serving(staleHit(1_000));

  expect(seen.scheduled).toHaveLength(1);
});

test("schedules nothing when Next says a hit is stale but the cache handler noted none", async () => {
  const seen = await serving((_req, res) => {
    res.setHeader("x-nextjs-cache", "STALE");
    res.end("stale page");
  });

  expect(seen.scheduled).toEqual([]);
});

test("has the cache handler read no page entry for a dynamic navigation of a partially static page", async () => {
  const seen = await serving(staleHit(1_000), { rsc: "1" }, "/shop?_rsc=x");

  expect(seen.served?.readsNoEntry).toBe(true);
});

test("schedules a refresh of the page after a dynamic navigation that read no page entry", async () => {
  const seen = await serving(staleHit(1_000), { rsc: "1" }, "/shop?_rsc=x");

  expect(seen.scheduled.map(({ url }) => url)).toEqual(["/shop"]);
});

test("lets a prefetch of a partially static page read its page entry", async () => {
  const seen = await serving(
    (_req, res) => res.end("shell"),
    { rsc: "1", "next-router-prefetch": "1" },
    "/shop?_rsc=x",
  );

  expect(seen.served?.readsNoEntry).toBe(false);
});

test("lets a navigation to a partially static page with static flight data read its page entry", async () => {
  const seen = await serving((_req, res) => res.end("flight"), { rsc: "1" }, "/catalog?_rsc=x");

  expect(seen.served?.readsNoEntry).toBe(false);
});

test("has the cache handler read no page entry for a server action posted to a partially static page", async () => {
  const seen = await serving(
    (_req, res) => res.end("action result"),
    { "next-action": "abc" },
    "/shop",
    "POST",
  );

  expect(seen.served?.readsNoEntry).toBe(true);
  expect(seen.purpose).toBeUndefined();
});

test("lets a dynamic navigation read its page entry where cache components are off", async () => {
  const seen = await serving(
    (_req, res) => res.end("flight"),
    { rsc: "1" },
    "/shop?_rsc=x",
    "GET",
    undefined,
    routesWithoutCacheComponents,
  );

  expect(seen.served?.readsNoEntry).toBe(false);
});

test("asks Next to serve a stale hit as it is on a page that never revalidates by time", async () => {
  const seen = await serving((_req, res) => res.end("page"), {}, "/about");

  expect(seen.purpose).toBe("prefetch");
});
