import http from "node:http";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { dispatchesAtOrigin } from "@framework/node-runtime/host";
import { afterAll, beforeAll, expect, test } from "vitest";
import {
  type DispatchHost,
  dispatchRequest,
  readDispatchHost,
  siblingFunctionUrls,
  withoutClientControl,
} from "../src/dispatch-host.mjs";

const LOCAL_BUNDLE = "local-bundle";
const SIBLING_BUNDLE = "other-bundle";
const SIBLING_URL = "https://sibling.example";
const ROUTER_KIND = "api-gateway";

const emptyRoutes = {
  beforeMiddleware: [],
  beforeFiles: [],
  afterFiles: [],
  dynamicRoutes: [],
  onMatch: [],
  fallback: [],
};

const manifest: RoutingManifest = {
  entry: LOCAL_BUNDLE,
  buildId: "t",
  basePath: "",
  pathnames: ["/local", "/keyless", "/sibling"],
  routes: emptyRoutes,
  dispatch: {
    "/local": { kind: "function", id: LOCAL_BUNDLE, entryKey: "/local" },
    "/keyless": { kind: "function", id: LOCAL_BUNDLE },
    "/sibling": { kind: "function", id: SIBLING_BUNDLE, entryKey: "/sibling" },
  },
};

let local: http.Server;
let localOrigin: string;
let seen: http.IncomingHttpHeaders[] = [];
let signed: Request[] = [];

beforeAll(async () => {
  local = http.createServer((req, res) => {
    seen.push(req.headers);
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("local");
  });
  await new Promise<void>((resolve) => local.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const address = local.address();
  if (!address || typeof address === "string") throw new Error("no local port");
  localOrigin = `http://127.0.0.1:${address.port}`;
});

afterAll(() => new Promise<void>((resolve) => local.close(() => resolve())));

function host(): DispatchHost {
  const capturing = (async (input: Request) => {
    const request = new Request(input);
    if (request.url.startsWith(localOrigin)) return fetch(request);
    signed.push(request);
    return new Response("sibling", { status: 200 });
  }) as unknown as typeof fetch;

  return {
    manifest,
    routerKind: ROUTER_KIND,
    keepCacheTags: true,
    localOrigin,
    functionUrls: { [SIBLING_BUNDLE]: SIBLING_URL },
    slug: "p1",
    app: "web",
    appBuildId: "d1",
    assetPrefix: "",
    originFetch: capturing,
  };
}

function serving(path: string, headers: Record<string, string> = {}) {
  seen = [];
  signed = [];
  return dispatchRequest(new Request(`https://app.example${path}`, { headers }), host(), () => {});
}

const forged = {
  "x-ocel-entry": "/admin",
  "x-ocel-probe": "probe-value",
  "x-middleware-rewrite": "/admin",
  "x-middleware-subrequest": "middleware",
  "next-resume": "1",
  "x-ocel-refresh": "1000",
  "x-keep": "yes",
};

test("a local route dispatches in-process over the loopback origin", async () => {
  const response = await serving("/local");

  expect(response.status).toBe(200);
  expect(await response.text()).toBe("local");
  expect(seen).toHaveLength(1);
  expect(signed).toHaveLength(0);
});

test("a sibling route goes out through the origin fetch its host handed it", async () => {
  const response = await serving("/sibling", forged);

  expect(response.status).toBe(200);
  expect(seen).toHaveLength(0);
  expect(signed).toHaveLength(1);

  const request = signed[0]!;
  expect(request.url).toBe(`${SIBLING_URL}/sibling`);
  expect(request.headers.get("x-ocel-entry")).toBe("/sibling");
  expect(request.headers.get("x-middleware-rewrite")).toBeNull();
  expect(request.headers.get("next-resume")).toBeNull();
  expect(request.headers.get("x-ocel-refresh")).toBeNull();
});

test("a dispatch host serves assets from the bucket its host handed it", async () => {
  const { writeFile, mkdtemp } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const dir = await mkdtemp(join(tmpdir(), "ocel-dispatch-access-"));
  const path = join(dir, "routing-manifest.json");
  await writeFile(path, JSON.stringify(manifest));
  const assetBucket = { get: async () => null };
  const originFetch = (async () => new Response("sibling")) as unknown as typeof fetch;

  const built = readDispatchHost({ OCEL_ROUTING_MANIFEST: path }, localOrigin, {
    assetBucket,
    originFetch,
  });

  expect(built.assetBucket).toBe(assetBucket);
  expect(built.originFetch).toBe(originFetch);
});

test("an image request is answered by the image origin its host handed the dispatcher", async () => {
  const { writeFile, mkdtemp } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const dir = await mkdtemp(join(tmpdir(), "ocel-dispatch-image-"));
  const path = join(dir, "routing-manifest.json");
  await writeFile(
    path,
    JSON.stringify({
      ...manifest,
      images: {
        path: "/_next/image",
        deviceSizes: [64],
        imageSizes: [],
        formats: ["image/webp"],
        domains: [],
        remotePatterns: [],
        minimumCacheTTL: 60,
        maximumRedirects: 3,
        maximumResponseBody: 1024,
        dangerouslyAllowSVG: false,
        dangerouslyAllowLocalIP: false,
        contentSecurityPolicy: "",
        contentDispositionType: "inline",
        configHash: "c".repeat(64),
      },
    }),
  );
  const asked: { url: string; w: number }[] = [];
  const built = readDispatchHost({ OCEL_ROUTING_MANIFEST: path }, localOrigin, {
    originFetch: fetch,
    imageOrigin: async (payload) => {
      asked.push({ url: payload.url, w: payload.w });
      return new Response("resized", { headers: { "content-type": "image/webp" } });
    },
  });

  const response = await dispatchRequest(
    new Request("https://app.example/_next/image?url=%2Flogo.png&w=64&q=75", {
      headers: { accept: "image/webp" },
    }),
    built,
    () => {},
  );

  expect(response.status).toBe(200);
  expect(await response.text()).toBe("resized");
  expect(asked).toEqual([{ url: "/logo.png", w: 64 }]);
});

test("the control headers a client forges never reach the local origin", async () => {
  await serving("/local", forged);

  const headers = seen[0]!;
  expect(headers["x-middleware-rewrite"]).toBeUndefined();
  expect(headers["x-middleware-subrequest"]).toBeUndefined();
  expect(headers["next-resume"]).toBeUndefined();
  expect(headers["x-keep"]).toBe("yes");
  expect(headers["x-ocel-probe"]).toBe("probe-value");
});

test("the entry a client names is replaced by the entry the route resolves to", async () => {
  await serving("/local", forged);

  expect(seen[0]!["x-ocel-entry"]).toBe("/local");
});

test("a route that names no entry leaves the client unable to name one", async () => {
  await serving("/keyless", forged);

  expect(seen[0]!["x-ocel-entry"]).toBeUndefined();
});

test("withoutClientControl keeps everything the app is allowed to see", () => {
  const kept = withoutClientControl(
    new Headers({ ...forged, cookie: "sid=1", "x-middleware-skip": "1" }),
  );

  expect([...kept.keys()].sort()).toEqual(["cookie", "x-keep", "x-ocel-probe"]);
  expect(kept.get("x-ocel-entry")).toBeNull();
  expect(kept.get("next-resume")).toBeNull();
  expect(kept.get("x-middleware-skip")).toBeNull();
  expect(kept.get("x-ocel-probe")).toBe("probe-value");
});

test("only a deploy that declared origin dispatch hosts it", () => {
  expect(dispatchesAtOrigin({} as NodeJS.ProcessEnv)).toBe(false);
  expect(dispatchesAtOrigin({ OCEL_ORIGIN_DISPATCH: "" } as NodeJS.ProcessEnv)).toBe(false);
  expect(dispatchesAtOrigin({ OCEL_ORIGIN_DISPATCH: "1" } as NodeJS.ProcessEnv)).toBe(true);
});

test("origin dispatch without a routing manifest refuses to boot", () => {
  expect(() =>
    readDispatchHost({ OCEL_ROUTER_KIND: "cloudfront" }, localOrigin, { originFetch: fetch }),
  ).toThrow(/OCEL_ROUTING_MANIFEST/);
});

test("sibling urls arrive as a routeId-to-URL object", () => {
  expect(siblingFunctionUrls(undefined)).toEqual({});
  expect(siblingFunctionUrls(`{"a":"${SIBLING_URL}"}`)).toEqual({ a: SIBLING_URL });
  expect(() => siblingFunctionUrls("[]")).toThrow(/routeId-to-URL/);
  expect(() => siblingFunctionUrls('{"a":1}')).toThrow(/names no URL/);
});

test("the env names the entry function's own bundle as the loopback origin", async () => {
  const { writeFile, mkdtemp } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const dir = await mkdtemp(join(tmpdir(), "ocel-dispatch-host-"));
  const path = join(dir, "routing-manifest.json");
  await writeFile(path, JSON.stringify(manifest));

  const built = readDispatchHost(
    {
      OCEL_ROUTER_KIND: "cloudfront",
      OCEL_ROUTING_MANIFEST: path,
      OCEL_FUNCTION_URLS: JSON.stringify({ [SIBLING_BUNDLE]: SIBLING_URL }),
      OCEL_ASSET_PREFIX: "prod/shop/web/r0a1b2c3d/assets",
      OCEL_SLUG: "shop",
      OCEL_APP: "web",
      OCEL_BUILD_ID: "d1",
    },
    localOrigin,
    { originFetch: fetch },
  );

  expect(built.manifest.entry).toBe(LOCAL_BUNDLE);
  expect(built.routerKind).toBe("cloudfront");
  expect(built.functionUrls).toEqual({ [SIBLING_BUNDLE]: SIBLING_URL });
  expect(built.assetPrefix).toBe("prod/shop/web/r0a1b2c3d/assets");
  expect(built.assetBucket).toBeUndefined();
});

test("every dispatched response names the router that served it", async () => {
  for (const path of ["/local", "/sibling"]) {
    const response = await serving(path, forged);
    expect(response.headers.get("x-ocel-router")).toBe(ROUTER_KIND);
  }
});

test("the router an origin claims is replaced by the one in front of it", async () => {
  const marked = await dispatchRequest(
    new Request("https://app.example/sibling"),
    { ...host(), routerKind: "cloudflare" },
    () => {},
  );

  expect(marked.headers.get("x-ocel-router")).toBe("cloudflare");
  expect(await marked.text()).toBe("sibling");
});

test("dispatch handed no router marks nothing", async () => {
  const bare = await dispatchRequest(
    new Request("https://app.example/sibling"),
    { ...host(), routerKind: "" },
    () => {},
  );

  expect(bare.headers.get("x-ocel-router")).toBeNull();
});
