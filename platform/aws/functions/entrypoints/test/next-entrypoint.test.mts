import net from "node:net";
import { EventEmitter } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { writeNextProjectFixture } from "./next-project-fixture.mjs";
import { noteRevalidation } from "../src/next/revalidation-signal.mjs";

const launcherModule = `module.exports = {
  async handler(req, res, ctx) {
    if (req.url === "/__revalidate") {
      globalThis.__ocelTestRevalidate();
      res.end("ok");
      return;
    }
    if (req.url === "/__tagged" || req.url === "/__stale" || req.url === "/__private") {
      const copied = Object.assign({}, req.headers);
      copied[Symbol.for("ocel.next.origin-cache-tags.v1")].push("products", "_N_T_/products");
      if (req.url === "/__stale") {
        res.setHeader("x-nextjs-cache", "STALE");
        res.setHeader("cache-control", "s-maxage=60, stale-while-revalidate=600");
      }
      if (req.url === "/__private") {
        res.setHeader("x-nextjs-cache", "STALE");
        res.setHeader("cache-control", "private, no-store");
      }
      res.setHeader("content-type", "text/html; charset=utf-8");
      res.end("<html>ok</html>");
      return;
    }
    if (req.url.startsWith("/isr") || req.url === "/static" || req.url === "/blog/hello") {
      if (req.url === "/isr?fail=1") res.statusCode = 500;
      if (req.url === "/isr?cookie=1") res.setHeader("set-cookie", "sid=1");
      res.setHeader("content-type", "text/html; charset=utf-8");
      res.end("<html>page</html>");
      return;
    }
    if (req.url === "/__request-meta") {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify(ctx.requestMeta));
      return;
    }
    const cache = globalThis.__incrementalCache;
    res.setHeader("content-type", "application/json");
    if (!cache) {
      res.statusCode = 500;
      res.end(JSON.stringify({ error: "incrementalCache missing" }));
      return;
    }
    const key = await cache.generateSimpleCacheKey("launcher-invocation");
    const hit = await cache.get(key, { kind: "FETCH", revalidate: 60 });
    if (hit && hit.value && !hit.isStale) {
      res.end(JSON.stringify({ from: "cache", random: JSON.parse(hit.value.data.body) }));
      return;
    }
    const random = Math.random();
    await cache.set(
      key,
      {
        kind: "FETCH",
        data: { headers: {}, body: JSON.stringify(random), status: 200, url: "" },
        revalidate: 60,
      },
      { fetchCache: true },
    );
    res.end(JSON.stringify({ from: "render", random }));
  },
};
`;

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
  dir = await mkdtemp(join(tmpdir(), "ocel-next-entry-"));

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

  (globalThis as any).__ocelTestRevalidate = noteRevalidation;

  process.env.OCEL_ISR_PREFIX = "prod/shop/web/r0a1b2c3d/isr";
  process.env.OCEL_EDGE_KIND = "aws";
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

test("unstable_cache's flow works through the published incremental cache", async () => {
  const first = await fetch(`http://127.0.0.1:${port}/api/anything`);
  expect(first.status).toBe(200);
  const rendered = await first.json();
  expect(rendered.from).toBe("render");

  const second = await fetch(`http://127.0.0.1:${port}/api/anything`);
  expect(second.status).toBe(200);
  const cached = await second.json();
  expect(cached.from).toBe("cache");
  expect(cached.random).toBe(rendered.random);
});

test("the server-action self-fetch origin is the loopback the app bound", async () => {
  expect(process.env.__NEXT_PRIVATE_ORIGIN).toBe(`http://127.0.0.1:${port}`);

  const reached = await fetch(`${process.env.__NEXT_PRIVATE_ORIGIN}/api/anything`);
  expect(reached.status).toBe(200);
});

function requestMeta(init?: RequestInit): Promise<any> {
  return fetch(`http://127.0.0.1:${port}/__request-meta`, init).then((r) => r.json());
}

const resume = { method: "POST", headers: { "next-resume": "1" }, body: "[1,{}]" };

test("a PPR resume runs under minimal mode", async () => {
  expect(await requestMeta(resume)).toMatchObject({ minimalMode: true });
});

test.each([
  ["a document GET", undefined],
  ["a POST that is not a resume", { method: "POST", body: "x" }],
  ["a GET carrying the resume header", { headers: { "next-resume": "1" } }],
])("%s does not", async (_name, init) => {
  const meta = await requestMeta(init);

  expect(meta.minimalMode).toBeUndefined();
  expect(meta.relativeProjectDir).toBeTruthy();
});

test("announces a revalidation the request performed", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/__revalidate`);

  expect(res.headers.get("x-ocel-revalidated")).toBe("1");
  await res.text();
});

test("carries the tags the render collected, prefixed by the release", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/__tagged`);

  expect(res.headers.get("cache-tag")).toBe(
    "r0a1b2c3d|products,r0a1b2c3d|_N_T_/products",
  );
  await res.text();
});

test("clamps the cache-control of a response Next marked STALE", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/__stale`);

  expect(res.headers.get("cache-control")).toBe("s-maxage=0, must-revalidate");
  expect(res.headers.get("cache-tag")).toBe(
    "r0a1b2c3d|products,r0a1b2c3d|_N_T_/products",
  );
  await res.text();
});

test("leaves a stale response Next marked private alone", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/__private`);

  expect(res.headers.get("cache-control")).toBe("private, no-store");
  await res.text();
});

test("shapes an ISR page's s-maxage from its initialRevalidateSeconds", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/isr`);

  expect(res.headers.get("cache-control")).toBe(
    "s-maxage=60, stale-while-revalidate=600",
  );
  await res.text();
});

test("shapes a dynamic ISR route from its fallbackRevalidate", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/blog/hello`);

  expect(res.headers.get("cache-control")).toBe("s-maxage=120");
  await res.text();
});

test("leaves a route the prerender manifest does not revalidate uncached", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/static`);

  expect(res.headers.has("cache-control")).toBe(false);
  await res.text();
});

test("leaves an error page on a revalidating route uncached", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/isr?fail=1`);

  expect(res.status).toBe(500);
  expect(res.headers.has("cache-control")).toBe(false);
  await res.text();
});

test("leaves a response that sets a cookie uncached", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/isr?cookie=1`);

  expect(res.headers.has("cache-control")).toBe(false);
  await res.text();
});

test("announces nothing on a request that revalidated nothing", async () => {
  const res = await fetch(`http://127.0.0.1:${port}/__request-meta`);

  expect(res.headers.has("x-ocel-revalidated")).toBe(false);
  await res.text();
});
