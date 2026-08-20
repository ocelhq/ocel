import http from "node:http";
import { afterAll, beforeAll, expect, test } from "vitest";

import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";

import { edgeHeader, routerMode } from "../src/shared/edge-kind.mjs";
import {
  routerHostFromEnv,
  serveRouted,
  siblingFunctionUrls,
  withoutClientControl,
  type RouterHost,
} from "../src/next/router-host.mjs";
import { s3AssetBucket } from "../src/next/router-assets.mjs";
import { isLoopback, siblingOriginFetch } from "../src/next/router-signing.mjs";

const LOCAL_BUNDLE = "local-bundle";
const SIBLING_BUNDLE = "other-bundle";
const SIBLING_URL = "https://abc123.lambda-url.us-east-1.on.aws";
const EDGE_KIND = "api-gateway";

const credentials = {
  AWS_ACCESS_KEY_ID: "AKIAEXAMPLE",
  AWS_SECRET_ACCESS_KEY: "secret",
  AWS_SESSION_TOKEN: "session",
};

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
    "/local": { kind: "lambda", id: LOCAL_BUNDLE, entryKey: "/local" },
    "/keyless": { kind: "lambda", id: LOCAL_BUNDLE },
    "/sibling": { kind: "lambda", id: SIBLING_BUNDLE, entryKey: "/sibling" },
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
  await new Promise<void>((resolve) =>
    local.listen({ host: "127.0.0.1", port: 0 }, resolve),
  );
  const address = local.address();
  if (!address || typeof address === "string") throw new Error("no local port");
  localOrigin = `http://127.0.0.1:${address.port}`;
});

afterAll(() => new Promise<void>((resolve) => local.close(() => resolve())));

function host(): RouterHost {
  const capturing = (async (input: Request) => {
    const request = new Request(input);
    if (request.url.startsWith(localOrigin)) return fetch(request);
    signed.push(request);
    return new Response("sibling", { status: 200 });
  }) as unknown as typeof fetch;

  return {
    manifest,
    edgeKind: EDGE_KIND,
    keepCacheTags: true,
    localOrigin,
    functionUrls: { [SIBLING_BUNDLE]: SIBLING_URL },
    slug: "p1",
    app: "web",
    deploymentId: "d1",
    assetPrefix: "",
    originFetch: siblingOriginFetch(credentials, "us-east-1", capturing),
  };
}

function serving(path: string, headers: Record<string, string> = {}) {
  seen = [];
  signed = [];
  return serveRouted(
    new Request(`https://app.example${path}`, { headers }),
    host(),
    () => {},
  );
}

const forged = {
  "x-ocel-entry": "/admin",
  "x-ocel-request-id": "forged",
  "x-middleware-rewrite": "/admin",
  "x-middleware-subrequest": "middleware",
  "next-resume": "1",
  "x-keep": "yes",
};

test("a local route dispatches in-process over the loopback origin", async () => {
  const response = await serving("/local");

  expect(response.status).toBe(200);
  expect(await response.text()).toBe("local");
  expect(seen).toHaveLength(1);
  expect(signed).toHaveLength(0);
});

test("the control headers a client forges never reach the local origin", async () => {
  await serving("/local", forged);

  const headers = seen[0]!;
  expect(headers["x-ocel-request-id"]).toBeUndefined();
  expect(headers["x-middleware-rewrite"]).toBeUndefined();
  expect(headers["x-middleware-subrequest"]).toBeUndefined();
  expect(headers["next-resume"]).toBeUndefined();
  expect(headers["x-keep"]).toBe("yes");
});

test("the entry a client names is replaced by the entry the route resolves to", async () => {
  await serving("/local", forged);

  expect(seen[0]!["x-ocel-entry"]).toBe("/local");
});

test("a route that names no entry leaves the client unable to name one", async () => {
  await serving("/keyless", forged);

  expect(seen[0]!["x-ocel-entry"]).toBeUndefined();
});

test("a sibling route is signed against its Function URL", async () => {
  const response = await serving("/sibling", forged);

  expect(response.status).toBe(200);
  expect(seen).toHaveLength(0);
  expect(signed).toHaveLength(1);

  const request = signed[0]!;
  expect(request.url).toBe(`${SIBLING_URL}/sibling`);
  expect(request.headers.get("authorization")).toMatch(/^AWS4-HMAC-SHA256 /);
  expect(request.headers.get("x-amz-date")).toBeTruthy();
  expect(request.headers.get("x-amz-security-token")).toBe("session");
  expect(request.headers.get("x-ocel-entry")).toBe("/sibling");
  expect(request.headers.get("x-middleware-rewrite")).toBeNull();
  expect(request.headers.get("next-resume")).toBeNull();
});

test("withoutClientControl keeps everything the app is allowed to see", () => {
  const kept = withoutClientControl(
    new Headers({ ...forged, cookie: "sid=1", "x-ocel-edge": "cloudfront" }),
  );

  expect([...kept.keys()].sort()).toEqual(["cookie", "x-keep"]);
});

test("only a deploy that declared an origin router hosts one", () => {
  expect(routerMode({} as NodeJS.ProcessEnv)).toBe(false);
  expect(routerMode({ OCEL_ORIGIN_ROUTER: "" } as NodeJS.ProcessEnv)).toBe(false);
  expect(routerMode({ OCEL_ORIGIN_ROUTER: "1" } as NodeJS.ProcessEnv)).toBe(true);
});

test("router mode without a routing manifest refuses to boot", () => {
  expect(() => routerHostFromEnv({ OCEL_EDGE_KIND: "cloudfront" }, localOrigin)).toThrow(
    /OCEL_ROUTING_MANIFEST/,
  );
});

test("only the loopback origin goes unsigned", () => {
  expect(isLoopback("http://127.0.0.1:8080/page")).toBe(true);

  for (const url of [
    "https://127.0.0.1.evil.com/page",
    "http://127.0.0.1@evil.com/page",
    "http://localhost:8080/page",
    "http://[::1]/page",
    "not a url",
  ]) {
    expect(isLoopback(url)).toBe(false);
  }
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
  const dir = await mkdtemp(join(tmpdir(), "ocel-router-host-"));
  const path = join(dir, "routing-manifest.json");
  await writeFile(path, JSON.stringify(manifest));

  const built = routerHostFromEnv(
    {
      OCEL_EDGE_KIND: "cloudfront",
      OCEL_ROUTING_MANIFEST: path,
      OCEL_FUNCTION_URLS: JSON.stringify({ [SIBLING_BUNDLE]: SIBLING_URL }),
      OCEL_ASSET_PREFIX: "prod/shop/web/r0a1b2c3d/assets",
      OCEL_SLUG: "shop",
      OCEL_APP: "web",
      OCEL_DEPLOYMENT_ID: "d1",
    },
    localOrigin,
  );

  expect(built.manifest.entry).toBe(LOCAL_BUNDLE);
  expect(built.edgeKind).toBe("cloudfront");
  expect(built.functionUrls).toEqual({ [SIBLING_BUNDLE]: SIBLING_URL });
  expect(built.assetPrefix).toBe("prod/shop/web/r0a1b2c3d/assets");
  expect(built.assetBucket).toBeUndefined();
});

test("the asset store reads an object out of the release's S3 prefix", async () => {
  const asked: string[] = [];
  const doFetch = (async (input: Request | string) => {
    const url = typeof input === "string" ? input : input.url;
    asked.push(url);
    if (!url.endsWith("/index.html")) return new Response(null, { status: 404 });
    return new Response("<html/>", { status: 200, headers: { etag: '"abc"' } });
  }) as unknown as typeof fetch;

  const bucket = s3AssetBucket("assets-bucket", "us-east-1", doFetch);

  const hit = await bucket.get("prod/shop/web/r0a1b2c3d/assets/index.html");
  expect(hit?.httpEtag).toBe('"abc"');
  expect(asked[0]).toBe(
    "https://assets-bucket.s3.us-east-1.amazonaws.com/prod/shop/web/r0a1b2c3d/assets/index.html",
  );

  expect(await bucket.get("prod/shop/web/r0a1b2c3d/assets/missing.html")).toBeNull();
});

test("a broken bucket is not a missing page", async () => {
  const answering = (status: number) =>
    s3AssetBucket("assets-bucket", "us-east-1", (async () =>
      new Response(null, { status })) as unknown as typeof fetch);

  expect(await answering(404).get("assets/gone.html")).toBeNull();
  expect(await answering(403).get("assets/gone.html")).toBeNull();
  await expect(answering(500).get("assets/gone.html")).rejects.toThrow(/500/);
});

test("an asset bucket the function cannot read refuses to boot", async () => {
  const { writeFile, mkdtemp } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const dir = await mkdtemp(join(tmpdir(), "ocel-router-creds-"));
  const path = join(dir, "routing-manifest.json");
  await writeFile(path, JSON.stringify(manifest));

  const env = {
    OCEL_EDGE_KIND: "cloudfront",
    OCEL_ROUTING_MANIFEST: path,
    OCEL_ASSET_BUCKET: "assets-bucket",
    AWS_REGION: "us-east-1",
  };

  expect(() => routerHostFromEnv(env, localOrigin)).toThrow(/assets-bucket/);
  expect(
    routerHostFromEnv({ ...env, ...credentials }, localOrigin).assetBucket,
  ).toBeDefined();
});

test("a sibling call signs with the credentials the sandbox holds now", async () => {
  const rotating = { ...credentials };
  const seen: Request[] = [];
  const doFetch = (async (input: Request) => {
    seen.push(new Request(input));
    return new Response("sibling");
  }) as unknown as typeof fetch;

  const originFetch = siblingOriginFetch(rotating, "us-east-1", doFetch);
  await originFetch(`${SIBLING_URL}/sibling`);

  rotating.AWS_SESSION_TOKEN = "rotated";
  await originFetch(`${SIBLING_URL}/sibling`);

  expect(seen[0]!.headers.get("x-amz-security-token")).toBe("session");
  expect(seen[1]!.headers.get("x-amz-security-token")).toBe("rotated");
});

test("a sibling call with no credentials fails loudly", async () => {
  const originFetch = siblingOriginFetch({}, "us-east-1");

  await expect(originFetch(`${SIBLING_URL}/sibling`)).rejects.toThrow(/credentials/);
});

test("every routed response names the edge that served it", async () => {
  for (const path of ["/local", "/sibling"]) {
    const response = await serving(path, forged);
    expect(response.headers.get(edgeHeader)).toBe(EDGE_KIND);
  }
});

test("the edge an origin claims is replaced by the edge in front of it", async () => {
  const marked = await serveRouted(
    new Request("https://app.example/sibling"),
    { ...host(), edgeKind: "cloudflare" },
    () => {},
  );

  expect(marked.headers.get(edgeHeader)).toBe("cloudflare");
  expect(await marked.text()).toBe("sibling");
});

test("a router hosted behind no edge marks nothing", async () => {
  const bare = await serveRouted(
    new Request("https://app.example/sibling"),
    { ...host(), edgeKind: "" },
    () => {},
  );

  expect(bare.headers.get(edgeHeader)).toBeNull();
});
