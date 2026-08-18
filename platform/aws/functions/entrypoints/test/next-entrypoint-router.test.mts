import net from "node:net";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { writeNextProjectFixture } from "./next-project-fixture.mjs";

const ENTRY_BUNDLE = "page-bundle";

const originSecret = "3c2b1a09f8e7d6c5b4a39281706f5e4d";

const launcherModule = `module.exports = {
  async handler(req, res) {
    const path = req.url.split("?")[0];
    if (path === "/__stale" || path === "/__private") {
      req.headers[Symbol.for("ocel.next.origin-cache-tags.v1")].push("products", "_N_T_/products");
      if (path === "/__stale") {
        res.setHeader("x-nextjs-cache", "STALE");
        res.setHeader("cache-control", "s-maxage=60, stale-while-revalidate=600");
      }
      if (path === "/__private") {
        res.setHeader("x-nextjs-cache", "STALE");
        res.setHeader("cache-control", "private, no-store");
      }
      res.setHeader("content-type", "text/html; charset=utf-8");
      res.end("<html>ok</html>");
      return;
    }
    if (path === "/isr" || path === "/static" || path === "/blog/hello") {
      if (req.url === "/isr?fail=1") res.statusCode = 500;
      if (req.url === "/isr?cookie=1") res.setHeader("set-cookie", "sid=1");
      res.setHeader("content-type", "text/html; charset=utf-8");
      res.end("<html>page</html>");
      return;
    }
    res.setHeader("content-type", "application/json");
    if (req.headers["x-set-cookies"]) {
      res.setHeader("set-cookie", ["csrf=1; Path=/", "session=2; Path=/"]);
    }
    res.end(JSON.stringify({ url: req.url, headers: req.headers }));
  },
};
`;

const routedPaths = ["/page", "/isr", "/static", "/blog/hello", "/__stale", "/__private"];

const routingManifest = {
  entry: ENTRY_BUNDLE,
  buildId: "t",
  basePath: "",
  pathnames: routedPaths,
  routes: {
    beforeMiddleware: [],
    beforeFiles: [],
    afterFiles: [],
    dynamicRoutes: [],
    onMatch: [],
    fallback: [],
  },
  dispatch: Object.fromEntries(
    routedPaths.map((path) => [path, { kind: "lambda", id: ENTRY_BUNDLE, entryKey: path }]),
  ),
};

type Msg = { type: string; payload: any };

const messages: Msg[] = [];
const bus = new EventEmitter();
const controlConns = new Set<net.Socket>();
let controlServer: net.Server;
let dir: string;
let port: number;

function waitFor(pred: () => boolean, timeoutMs = 5000): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (pred()) return resolve();
    const onMsg = (): void => {
      if (pred()) {
        clearTimeout(timer);
        bus.off("msg", onMsg);
        resolve();
      }
    };
    const timer = setTimeout(() => {
      bus.off("msg", onMsg);
      reject(new Error(`timeout; messages so far: ${JSON.stringify(messages)}`));
    }, timeoutMs);
    bus.on("msg", onMsg);
  });
}

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-next-router-"));

  const sockPath = join(dir, "control.sock");
  controlServer = net.createServer((conn) => {
    controlConns.add(conn);
    conn.on("close", () => controlConns.delete(conn));
    let buf = "";
    conn.on("data", (d) => {
      buf += d.toString();
      let idx: number;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx);
        buf = buf.slice(idx + 1);
        if (line.trim()) {
          messages.push(JSON.parse(line));
          bus.emit("msg");
        }
      }
    });
  });
  await new Promise<void>((resolve) => controlServer.listen(sockPath, resolve));

  const projectDir = join(dir, "project");
  await writeNextProjectFixture(
    projectDir,
    {},
    {
      routes: {
        "/isr": { initialRevalidateSeconds: 60, initialExpireSeconds: 660 },
        "/static": { initialRevalidateSeconds: false },
      },
      dynamicRoutes: {
        "/blog/[slug]": {
          routeRegex: "^/blog/([^/]+?)(?:/)?$",
          fallbackRevalidate: 120,
        },
      },
    },
  );
  const launcherPath = join(projectDir, "__next_launcher.cjs");
  await writeFile(launcherPath, launcherModule);

  const manifestPath = join(dir, "routing-manifest.json");
  await writeFile(manifestPath, JSON.stringify(routingManifest));

  process.env.OCEL_ISR_PREFIX = "prod/shop/web/r0a1b2c3d/isr";
  process.env.OCEL_EDGE_KIND = "native";
  process.env.OCEL_ORIGIN_SECRET = originSecret;
  process.env.OCEL_ROUTING_MANIFEST = manifestPath;
  process.env.OCEL_CONTROL_SOCKET = sockPath;
  process.env.OCEL_HANDLER = launcherPath;
  await import("../src/next/entrypoint.mjs");
  await waitFor(() => messages.some((m) => m.type === "server-ready"));
  port = messages.find((m) => m.type === "server-ready")!.payload.httpPort;
});

afterAll(async () => {
  for (const c of controlConns) c.end();
  await new Promise<void>((resolve) => controlServer.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

function front(path: string, init: RequestInit = {}): Promise<Response> {
  return fetch(`http://127.0.0.1:${port}${path}`, {
    ...init,
    headers: { ...(init.headers as Record<string, string>), "x-ocel-origin-secret": originSecret },
  });
}

test("the unsigned front door refuses a request the origin secret does not open", async () => {
  const response = await fetch(`http://127.0.0.1:${port}/page`);

  expect(response.status).toBe(403);
  await response.text();
});

test("the announced server is the router, not the app's own loopback", () => {
  expect(process.env.__NEXT_PRIVATE_ORIGIN).not.toBe(`http://127.0.0.1:${port}`);
});

test("a routed request reaches the app through the entry the manifest names", async () => {
  const response = await front(`/page`, {
    headers: {
      "x-ocel-entry": "/admin",
      "x-middleware-subrequest": "middleware",
      "next-resume": "1",
      "x-keep": "yes",
    },
  });

  expect(response.status).toBe(200);
  const seen = await response.json();
  expect(seen.url).toBe("/page");
  expect(seen.headers["x-ocel-entry"]).toBe("/page");
  expect(seen.headers["x-middleware-subrequest"]).toBeUndefined();
  expect(seen.headers["next-resume"]).toBeUndefined();
  expect(seen.headers["x-keep"]).toBe("yes");
});

test("every Set-Cookie the app writes survives the router", async () => {
  const response = await front(`/page`, {
    headers: { "x-set-cookies": "1" },
  });

  expect(response.headers.getSetCookie()).toEqual([
    "csrf=1; Path=/",
    "session=2; Path=/",
  ]);
});

test("a forwarded host a client forges is not the host the app answers as", async () => {
  const response = await front(`/page`, {
    headers: { "x-forwarded-host": "evil.example", "x-forwarded-proto": "https" },
  });

  const seen = await response.json();
  expect(seen.headers.host).not.toContain("evil.example");
  expect(seen.headers["x-forwarded-host"]).not.toBe("evil.example");
});

test("a pathname the manifest does not route is a 404 the router answers", async () => {
  const response = await front(`/nowhere`);

  expect(response.status).toBe(404);
});

test("clamps the cache-control of a response Next marked STALE", async () => {
  const res = await front(`/__stale`);

  expect(res.headers.get("cache-control")).toBe("s-maxage=0, must-revalidate");
  await res.text();
});

test("leaves a stale response Next marked private alone", async () => {
  const res = await front(`/__private`);

  expect(res.headers.get("cache-control")).toBe("private, no-store");
  await res.text();
});

test("shapes an ISR page's s-maxage from its initialRevalidateSeconds", async () => {
  const res = await front(`/isr`);

  expect(res.headers.get("cache-control")).toBe(
    "s-maxage=60, stale-while-revalidate=600",
  );
  await res.text();
});

test("shapes a dynamic ISR route from its fallbackRevalidate", async () => {
  const res = await front(`/blog/hello`);

  expect(res.headers.get("cache-control")).toBe("s-maxage=120");
  await res.text();
});

test("leaves a route the prerender manifest does not revalidate uncached", async () => {
  const res = await front(`/static`);

  expect(res.headers.has("cache-control")).toBe(false);
  await res.text();
});

test("leaves an error page on a revalidating route uncached", async () => {
  const res = await front(`/isr?fail=1`);

  expect(res.status).toBe(500);
  expect(res.headers.has("cache-control")).toBe(false);
  await res.text();
});

test("leaves a response that sets a cookie uncached", async () => {
  const res = await front(`/isr?cookie=1`);

  expect(res.headers.has("cache-control")).toBe(false);
  await res.text();
});
