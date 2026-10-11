import { mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import type { NextRouteTable } from "@framework/next-protocol/route-table";
import { dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { afterAll, beforeAll, expect, test } from "vitest";
import { newGcpDispatchInvoke, readGcpDispatchHost } from "../src/next/dispatch-host.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";
import { type ServedBucket, serveBucket } from "./serve-bucket.mjs";

const manifest: NextRouteTable = {
  rootFunction: "bundle-0",
  buildId: "b1",
  basePath: "",
  pathnames: ["/home", "/other", "/gzipped", "/logo.svg"],
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
    "/gzipped": { kind: "function", id: "bundle-0", entryKey: "/gzipped" },
    "/logo.svg": { kind: "static" },
  },
};

const bucket = newCloudStorageBucket();
let stored: ServedBucket;
let assetEnv: NodeJS.ProcessEnv;
let dir: string;
let local: http.Server;
let localOrigin: string;
const served: string[] = [];
const gzipped = gzipSync(Buffer.from("compressed by the app"));

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-dispatch-"));
  await writeFile(join(dir, "routing.json"), JSON.stringify(manifest));
  bucket.objects.set("assets/prod/shop/web/r1/assets/logo.svg", {
    body: "<svg/>",
    generation: "3",
  });
  stored = await serveBucket(bucket);
  assetEnv = {
    OCEL_ASSET_BUCKET: "ocel-acme-production",
    OCEL_STORAGE_ENDPOINT: stored.endpoint,
    OCEL_ASSET_PREFIX: "prod/shop/web/r1/assets",
  };
  local = http.createServer((req, res) => {
    if (req.headers["x-ocel-entry"] === "/gzipped") {
      res.writeHead(200, { "content-encoding": "gzip", "content-length": gzipped.length });
      res.end(gzipped);
      return;
    }
    served.push(String(req.headers["x-ocel-entry"]));
    res.end("rendered here");
  });
  await new Promise<void>((resolve) => local.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const { port } = local.address() as { port: number };
  localOrigin = `http://127.0.0.1:${port}`;
});

afterAll(async () => {
  await new Promise<void>((resolve) => local.close(() => resolve()));
  await stored.close();
  await rm(dir, { recursive: true, force: true });
});

test("every function a route names is rendered by the instance that routed it", async () => {
  const host = readGcpDispatchHost(
    { OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json") },
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

test("a body the app gzipped itself reaches the client with its own bytes and encoding", async () => {
  const host = readGcpDispatchHost(
    { OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json") },
    localOrigin,
  );

  const response = await dispatchRequest(
    new Request("https://shop.example/gzipped", { headers: { "accept-encoding": "gzip" } }),
    host,
    () => {},
  );

  expect(response.headers.get("content-encoding")).toBe("gzip");
  expect(Buffer.from(await response.arrayBuffer())).toEqual(gzipped);
});

test("a static asset is served from the bucket the service is told holds it", async () => {
  const host = readGcpDispatchHost(
    { OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json"), ...assetEnv },
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

test("a static flight response reaches Cloud CDN without the router state tree in its Vary", async () => {
  const invoke = newGcpDispatchInvoke(localOrigin, {
    OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json"),
    ...assetEnv,
  });
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
  const invoke = newGcpDispatchInvoke(localOrigin, {
    OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json"),
    OCEL_CACHE_TAG_PURGE: "1",
    OCEL_ISR_PREFIX: "production/shop/web/r1a2b3c4d/isr",
  });
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
