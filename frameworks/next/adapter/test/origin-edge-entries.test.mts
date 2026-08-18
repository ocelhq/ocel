import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";
import { afterEach, expect, test, vi } from "vitest";

import {
  buildOriginEdgeApp,
  listenOn,
  serveDispatch,
  type RunningServer,
} from "./origin-edge-app.mjs";

const running: RunningServer[] = [];

afterEach(async () => {
  await Promise.all(running.splice(0).map((server) => server.close()));
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

async function started(server: Promise<RunningServer>): Promise<RunningServer> {
  const ready = await server;
  running.push(ready);
  return ready;
}

test("compiles waived edge middleware into a Node entry on a non-programmable edge", async () => {
  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
  });

  expect(app.manifest.middleware).toMatchObject({
    runtime: "nodejs",
    entryKey: "/_middleware",
  });
  expect(app.manifest.middleware?.matchers).toEqual([
    { source: "/:path*", sourceRegex: "^(?:/(.*))?$" },
  ]);
  expect(app.hasEdgeBundle).toBe(false);

  const launcher = await readFile(
    join(app.funcDir(app.manifest.middleware!.id), "__next_launcher.cjs"),
    "utf8",
  );
  expect(launcher).toContain('"/_middleware":"./__ocel_edge_1.cjs"');
});

test("runs the compiled middleware entry in plain Node, with no edge sandbox", async () => {
  const app = await buildOriginEdgeApp({ allowDegraded: "edge-middleware" });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  const response = await fetch(`${origin.origin}/dashboard?a=1`, {
    headers: {
      "x-ocel-entry": "/_middleware",
      "x-forwarded-host": "app.example",
    },
    redirect: "manual",
  });

  expect(response.headers.get("x-middleware-next")).toBe("1");
  expect(response.headers.get("x-mw-url")).toBe(
    "https://app.example/dashboard?a=1",
  );
  expect(response.headers.get("x-mw-self")).toBe("true");
  expect(response.headers.get("x-mw-build")).toBe("test-build");
});

test("dispatches a waived edge route to the bundle that carries its compiled entry", async () => {
  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
  });

  expect(app.manifest.dispatch["/api/edge"]).toEqual({
    kind: "lambda",
    id: app.manifest.middleware!.id,
    entryKey: "middleware_app/api/edge/route",
  });
});

test("serves a waived edge route through the bundle's own dispatch", async () => {
  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
    edgeRouteHandler: `async (request) =>
      new Response(await request.text(), {
        status: 201,
        headers: {
          "x-echo-method": request.method,
          "x-echo-agent": request.headers.get("x-agent") ?? "none",
          "x-echo-entry": String(globalThis.__OCEL_EDGE_ENTRY),
        },
      })`,
  });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  const response = await fetch(`${origin.origin}/api/edge`, {
    method: "POST",
    headers: { "x-agent": "probe" },
    body: "ping",
  });

  expect(response.status).toBe(201);
  expect(response.headers.get("x-echo-method")).toBe("POST");
  expect(response.headers.get("x-echo-agent")).toBe("probe");
  expect(response.headers.get("x-echo-entry")).toBe(
    "middleware_app/api/edge/route",
  );
  expect(await response.text()).toBe("ping");
});

test("a waived edge route proxying an encoded upstream emits a body its headers describe", async () => {
  const body = "compress me, compress me, compress me";
  const upstream = await started(
    listenOn((_req, res) => {
      const packed = gzipSync(body);
      res.writeHead(200, {
        "content-type": "text/plain",
        "content-encoding": "gzip",
        "content-length": String(packed.byteLength),
        te: "trailers",
        "proxy-authenticate": "Basic",
      });
      res.end(packed);
    }),
  );

  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
    edgeRouteHandler: `async () => fetch(${JSON.stringify(upstream.origin)})`,
  });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  const response = await fetch(`${origin.origin}/api/edge`);

  expect(await response.text()).toBe(body);
  expect(response.headers.get("content-encoding")).toBe(null);
  expect(response.headers.get("te")).toBe(null);
  expect(response.headers.get("proxy-authenticate")).toBe(null);
  expect(response.headers.get("content-type")).toBe("text/plain");
});

test("a waived edge middleware proxying an encoded upstream emits a body its headers describe", async () => {
  const body = "middleware answered, middleware answered";
  const upstream = await started(
    listenOn((_req, res) => {
      const packed = gzipSync(body);
      res.writeHead(403, {
        "content-type": "text/plain",
        "content-encoding": "gzip",
        "content-length": String(packed.byteLength),
        te: "trailers",
        "proxy-authenticate": "Basic",
      });
      res.end(packed);
    }),
  );

  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware",
    middlewareHandler: `async () => fetch(${JSON.stringify(upstream.origin)})`,
  });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  const response = await fetch(`${origin.origin}/dashboard`, {
    headers: { "x-ocel-entry": "/_middleware" },
  });

  expect(response.status).toBe(403);
  expect(await response.text()).toBe(body);
  expect(response.headers.get("content-encoding")).toBe(null);
  expect(response.headers.get("te")).toBe(null);
  expect(response.headers.get("proxy-authenticate")).toBe(null);
  expect(response.headers.get("x-ocel-middleware-headers")).toBe("content-type");
});

test("a waived edge route reads an edge asset the bundle carries", async () => {
  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
    edgeAssets: { "greeting.txt": "hello from the asset" },
    edgeRouteHandler: `async () =>
      new Response(await (await fetch("blob:greeting.txt")).text())`,
  });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  const response = await fetch(`${origin.origin}/api/edge`);

  expect(await response.text()).toBe("hello from the asset");
});

test("a waived edge route's compiled cache handler writes without throwing", async () => {
  const cacheHandler = fileURLToPath(
    new URL("../src/edge-cache-handler.cjs", import.meta.url),
  );
  const app = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
    edgeRouteHandler: `async () => {
      process.env.NEXT_RUNTIME = "edge"
      const CacheHandler = require(${JSON.stringify(cacheHandler)})
      const cache = new CacheHandler()
      await cache.set(
        "k",
        { kind: "FETCH", data: { body: "x" }, revalidate: 60 },
        { tags: [] },
      )
      await cache.revalidateTag(["t"])
      return new Response(String(await cache.get("k", { kind: "FETCH" })))
    }`,
  });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  try {
    const response = await fetch(`${origin.origin}/api/edge`);
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("null");
  } finally {
    delete process.env.NEXT_RUNTIME;
  }
});

test("keeps a waived edge entry's own env out of the process it shares", async () => {
  vi.stubEnv("__NEXT_BUILD_ID", "already-running");
  const app = await buildOriginEdgeApp({ allowDegraded: "edge-middleware" });
  const origin = await started(
    serveDispatch(app.dispatchIn(app.manifest.middleware!.id)),
  );

  const response = await fetch(`${origin.origin}/dashboard`, {
    headers: { "x-ocel-entry": "/_middleware" },
  });

  expect(response.headers.get("x-mw-build")).toBe("already-running");
});

test("warns that revalidate is inert for a prerender a waived edge route parents", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
    edgePrerender: "/edge-page",
  });

  const inert = warn.mock.calls
    .map((call) => String(call[0]))
    .filter((line) => line.includes("revalidate is inert"));
  expect(inert).toHaveLength(1);
  expect(inert[0]).toContain("/edge-page");
});

test("keeps the edge bundle and the edge middleware entry on a programmable edge", async () => {
  const app = await buildOriginEdgeApp({
    edgeKind: "cloudflare",
    allowDegraded: "edge-middleware,edge-runtime",
  });

  expect(app.manifest.middleware).toMatchObject({
    runtime: "edge",
    entryKey: "middleware_middleware",
  });
  expect(app.manifest.dispatch["/api/edge"]).toEqual({
    kind: "edge",
    entryKey: "middleware_app/api/edge/route",
  });
  expect(app.hasEdgeBundle).toBe(true);
});

test("emits neither an edge bundle nor a Node entry when the need is not waived", async () => {
  const app = await buildOriginEdgeApp();

  expect(app.manifest.middleware).toMatchObject({ runtime: "edge" });
  expect(app.hasEdgeBundle).toBe(false);
  await expect(
    readFile(join(app.funcDir("bundle-0"), "__ocel_edge_0.cjs")),
  ).rejects.toThrow();
});

test("declares the degraded needs whether or not they are waived", async () => {
  const waived = await buildOriginEdgeApp({
    allowDegraded: "edge-middleware,edge-runtime",
  });
  expect(Object.keys(waived.serve.needs)).toContain("edge-middleware");
  expect(waived.serve.needs["edge-runtime"]?.routes).toEqual(["/api/edge"]);

  const unwaived = await buildOriginEdgeApp();
  expect(Object.keys(unwaived.serve.needs)).toContain("edge-middleware");
  expect(unwaived.serve.needs["edge-runtime"]?.routes).toEqual(["/api/edge"]);
});
