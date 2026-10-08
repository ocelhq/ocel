import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { afterAll, beforeAll, expect, test } from "vitest";
import { newGcpDispatchInvoke, readGcpDispatchHost } from "../src/next/dispatch-host.mjs";
import { newRefreshEndpoint } from "../src/next/refresh-endpoint.mjs";
import { refreshSignatureHeader, signRefreshTask } from "../src/next/refresh-signature.mjs";

const manifest: RoutingManifest = {
  entry: "bundle-0",
  buildId: "b1",
  basePath: "",
  pathnames: ["/home", "/other", "/logo.svg"],
  routes: {
    beforeMiddleware: [],
    beforeFiles: [],
    afterFiles: [],
    dynamicRoutes: [],
    onMatch: [],
    fallback: [],
  },
  dispatch: {
    "/home": { kind: "function", id: "bundle-0", entryKey: "/home" },
    "/other": { kind: "function", id: "bundle-1", entryKey: "/other" },
    "/logo.svg": { kind: "static" },
  },
};

let dir: string;
let local: http.Server;
let localOrigin: string;
const served: string[] = [];
const refreshed: string[] = [];

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-dispatch-"));
  await writeFile(join(dir, "routing.json"), JSON.stringify(manifest));
  await mkdir(join(dir, "static", "assets"), { recursive: true });
  await writeFile(join(dir, "static", "assets", "logo.svg"), "<svg/>");
  local = http.createServer((req, res) => {
    served.push(String(req.headers["x-ocel-entry"]));
    if (req.headers["x-ocel-refresh"] !== undefined) refreshed.push(String(req.url));
    res.end("rendered here");
  });
  await new Promise<void>((resolve) => local.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const { port } = local.address() as { port: number };
  localOrigin = `http://127.0.0.1:${port}`;
});

afterAll(async () => {
  await new Promise<void>((resolve) => local.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
});

test("every function a route names is rendered by the instance that routed it", async () => {
  const host = readGcpDispatchHost(
    { OCEL_ROUTING_MANIFEST: join(dir, "routing.json") },
    localOrigin,
  );

  const responses = await Promise.all(
    ["/home", "/other"].map((path) =>
      dispatchRequest(new Request(`https://shop.example${path}`), host, () => {}),
    ),
  );

  expect(await Promise.all(responses.map((response) => response.text()))).toEqual([
    "rendered here",
    "rendered here",
  ]);
  expect(served.sort()).toEqual(["/home", "/other"]);
});

test("a static asset is served from the directory the service's image holds it in", async () => {
  const host = readGcpDispatchHost(
    {
      OCEL_ROUTING_MANIFEST: join(dir, "routing.json"),
      OCEL_STATIC_DIR: join(dir, "static"),
      OCEL_ASSET_PREFIX: "prod/shop/web/r1/assets",
    },
    localOrigin,
  );

  const response = await dispatchRequest(
    new Request("https://shop.example/logo.svg"),
    host,
    () => {},
  );

  expect(response.status).toBe(200);
  expect(response.headers.get("content-type")).toBe("image/svg+xml");
  expect(await response.text()).toBe("<svg/>");
});

async function serveInvoke(invoke: ReturnType<typeof newGcpDispatchInvoke>): Promise<http.Server> {
  const server = http.createServer((req, res) => {
    Promise.resolve(invoke(req, res, { waitUntil() {} })).catch(() => {
      res.statusCode = 500;
      res.end();
    });
  });
  await new Promise<void>((resolve) => server.listen({ host: "127.0.0.1", port: 0 }, resolve));
  return server;
}

function urlOf(server: http.Server): string {
  return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
}

test("a refresh task reaches the endpoint before the router strips its control headers", async () => {
  const endpoint = newRefreshEndpoint({
    path: "/_ocel/refresh",
    isrPrefix: "prod/shop/web/r1/isr",
    secret: "s1",
    localOrigin,
    check: async () => true,
    readEntry: async () => ({ lastModified: 10, value: {} }),
  });
  const server = await serveInvoke(
    newGcpDispatchInvoke(
      localOrigin,
      { OCEL_ROUTING_MANIFEST: join(dir, "routing.json") },
      endpoint,
    ),
  );
  try {
    const body = JSON.stringify({
      isrPrefix: "prod/shop/web/r1/isr",
      refresh: { url: "/home", key: "home", lastModified: 5, headers: { host: "shop.example" } },
    });
    const response = await fetch(`${urlOf(server)}/_ocel/refresh`, {
      method: "POST",
      headers: {
        authorization: "Bearer any",
        [refreshSignatureHeader]: signRefreshTask("s1", Buffer.from(body)),
      },
      body,
    });

    expect(response.status).toBe(204);
    expect(refreshed).toEqual(["/home"]);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("a service told no refresh url routes the refresh path like any other", async () => {
  const server = await serveInvoke(
    newGcpDispatchInvoke(
      localOrigin,
      { OCEL_ROUTING_MANIFEST: join(dir, "routing.json") },
      undefined,
    ),
  );
  try {
    const response = await fetch(`${urlOf(server)}/_ocel/refresh`);

    expect(response.status).not.toBe(405);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test("a static flight response reaches Cloud CDN without the router state tree in its Vary", async () => {
  const invoke = newGcpDispatchInvoke(
    localOrigin,
    {
      OCEL_ROUTING_MANIFEST: join(dir, "routing.json"),
      OCEL_STATIC_DIR: join(dir, "static"),
      OCEL_ASSET_PREFIX: "prod/shop/web/r1/assets",
    },
    undefined,
  );
  const edge = http.createServer((req, res) => invoke(req, res, { waitUntil: () => {} }));
  await new Promise<void>((resolve) => edge.listen({ host: "127.0.0.1", port: 0 }, resolve));
  try {
    const { port } = edge.address() as { port: number };
    const response = await fetch(`http://127.0.0.1:${port}/logo.svg`, { headers: { rsc: "1" } });
    const vary = response.headers.get("vary") ?? "";

    expect(response.status).toBe(200);
    expect(vary).toContain("rsc");
    expect(vary).toContain("next-url");
    expect(vary).not.toContain("next-router-state-tree");
  } finally {
    await new Promise<void>((resolve) => edge.close(() => resolve()));
  }
});

test("a page the GCP dispatch serves carries its release tag when its edge purges by tag", async () => {
  const invoke = newGcpDispatchInvoke(
    localOrigin,
    {
      OCEL_ROUTING_MANIFEST: join(dir, "routing.json"),
      OCEL_CACHE_TAG_PURGE: "1",
      OCEL_ISR_PREFIX: "production/shop/web/r1a2b3c4d/isr",
    },
    undefined,
  );
  const edge = http.createServer((req, res) => invoke(req, res, { waitUntil: () => {} }));
  await new Promise<void>((resolve) => edge.listen({ host: "127.0.0.1", port: 0 }, resolve));
  try {
    const { port } = edge.address() as { port: number };
    const response = await fetch(`http://127.0.0.1:${port}/home`);

    expect(response.status).toBe(200);
    expect(response.headers.get("cache-tag")?.split(",")[0]).toBe("r1a2b3c4d");
  } finally {
    await new Promise<void>((resolve) => edge.close(() => resolve()));
  }
});
