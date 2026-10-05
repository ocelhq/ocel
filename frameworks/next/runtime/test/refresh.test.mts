import http from "node:http";
import { refreshHeader } from "@framework/next-cache";
import { afterEach, expect, test } from "vitest";
import { revalidatingRoutes } from "../src/cache-shaping.mjs";
import { noteServedEntry, type Refresh, routeStaleHitsToRefresh } from "../src/refresh.mjs";

const routes = revalidatingRoutes({
  config: {},
  distDir: ".next",
  prerender: {
    routes: { "/blog": { initialRevalidateSeconds: 60 } },
    dynamicRoutes: {
      "/posts/[slug]": { routeRegex: "^/posts/([^/]+?)(?:/)?$", fallbackRevalidate: 60 },
    },
  },
});

let server: http.Server | undefined;

afterEach(async () => {
  await new Promise<void>((resolve) => (server ? server.close(() => resolve()) : resolve()));
  server = undefined;
});

interface Seen {
  purpose: string | undefined;
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
): Promise<Seen> {
  const seen: Seen = { purpose: undefined, scheduled: [], waited: [], status: undefined, body: "" };
  server = http.createServer((req, res) => {
    routeStaleHitsToRefresh(
      req,
      res,
      routes,
      schedule ??
        (async (refresh) => {
          seen.scheduled.push(refresh);
        }),
      (promise) => seen.waited.push(promise),
    );
    seen.purpose = req.headers.purpose as string | undefined;
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

function staleHit(lastModified: number) {
  return (req: http.IncomingMessage, res: http.ServerResponse) => {
    noteServedEntry(req.headers as Record<string | symbol, any>, lastModified);
    res.setHeader("x-nextjs-cache", "STALE");
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
      lastModified: 1_000,
      headers: { host: "shop.example", [refreshHeader]: "1000" },
    },
  ]);
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
  const seen = await serving((req, res) => {
    noteServedEntry(req.headers as Record<string | symbol, any>, 1_000);
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

test("leaves a request to a route that never revalidates as Next would see it", async () => {
  const seen = await serving((_req, res) => res.end("dynamic page"), {}, "/dashboard");

  expect(seen.purpose).toBeUndefined();
});

test("leaves a server action posted to a revalidating route as Next would see it", async () => {
  const seen = await serving((_req, res) => res.end("action result"), {}, "/blog", "POST");

  expect(seen.purpose).toBeUndefined();
});
