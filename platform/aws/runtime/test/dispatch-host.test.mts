import http from "node:http";
import v8 from "node:v8";
import vm from "node:vm";
import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import { type DispatchHost, dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { afterAll, beforeAll, expect, test } from "vitest";
import { s3AssetBucket } from "../src/next/dispatch-assets.mjs";
import { awsDispatchAccess } from "../src/next/dispatch-host.mjs";
import { isLoopback, siblingOriginFetch } from "../src/next/dispatch-signing.mjs";

const LOCAL_BUNDLE = "local-bundle";
const SIBLING_BUNDLE = "other-bundle";
const SIBLING_URL = "https://abc123.lambda-url.us-east-1.on.aws";
const ROUTER_KIND = "api-gateway";

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
    deploymentId: "d1",
    assetPrefix: "",
    originFetch: siblingOriginFetch(credentials, "us-east-1", capturing),
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
  "x-keep": "yes",
};

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

test("an asset body stays readable after the S3 response it came from is collected", async () => {
  v8.setFlagsFromString("--expose-gc");
  const collectGarbage = vm.runInNewContext("gc") as () => void;
  const viaLocal = ((input: Request | string) => {
    const url = new URL(typeof input === "string" ? input : input.url);
    return fetch(new URL(url.pathname, localOrigin));
  }) as typeof fetch;
  const bucket = s3AssetBucket("assets-bucket", "us-east-1", viaLocal);

  const hit = await bucket.get("assets/index.html");
  await new Promise((resolve) => setTimeout(resolve, 0));
  collectGarbage();
  await new Promise((resolve) => setTimeout(resolve, 0));

  expect(await new Response(hit!.body).text()).toBe("local");
});

test("a broken bucket is not a missing page", async () => {
  const answering = (status: number) =>
    s3AssetBucket(
      "assets-bucket",
      "us-east-1",
      (async () => new Response(null, { status })) as unknown as typeof fetch,
    );

  expect(await answering(404).get("assets/gone.html")).toBeNull();
  expect(await answering(403).get("assets/gone.html")).toBeNull();
  await expect(answering(500).get("assets/gone.html")).rejects.toThrow(/500/);
});

test("an asset bucket the function cannot read refuses to boot", () => {
  const env = { OCEL_ASSET_BUCKET: "assets-bucket", AWS_REGION: "us-east-1" };

  expect(() => awsDispatchAccess(env)).toThrow(/assets-bucket/);
  expect(awsDispatchAccess({ ...env, ...credentials }).assetBucket).toBeDefined();
});

test("a sibling call signs with the credentials the sandbox has now", async () => {
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
