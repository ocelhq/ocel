import { describe, expect, it } from "vitest";

import { serve, type RouteDeps } from "../src/index";
import type { AssetBucket } from "@framework/next-router/assets";
import type { CacheDeps } from "../src/cache";
import type { ImageConfig } from "@framework/next-router/image";
import fixtures from "@framework/next-router/fixtures/image-conformance.json";
import { coloDeps } from "./cache-deps";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]
  .config;

const ASSET_PREFIX = "prod/p1/web/r3f8a1c9d/assets";

function assetStoreServing(files: Record<string, string>): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      const body = files[key.slice(ASSET_PREFIX.length)];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: ASSET_PREFIX,
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

const emptyRoutes = {
  beforeMiddleware: [],
  beforeFiles: [],
  afterFiles: [],
  dynamicRoutes: [],
  onMatch: [],
  fallback: [],
};

function imageDeps(overrides: Partial<RouteDeps> = {}): RouteDeps {
  return {
    manifest: {
      buildId: "b1",
      basePath: "",
      pathnames: [],
      routes: emptyRoutes,
      dispatch: {},
      images: BASE_CONFIG,
    },
    functionUrls: {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: assetStoreServing({ "/404.html": "<h1>not found</h1>" }),
    ...overrides,
  };
}

const imageRequest = (url: string, init?: RequestInit) => new Request(url, init);

describe("the /_next/image route", () => {
  it("is handled ahead of the static-asset fallthrough", async () => {
    const response = await serve(
      imageRequest("https://app.example/_next/image?url=%2Fa.png&w=999&q=75"),
      imageDeps(),
    );

    expect(response.status).toBe(400);
    expect(await response.text()).toBe('"w" parameter (width) of 999 is not allowed');
  });

  it("is not registered when the build emitted no image config", async () => {
    const deps = imageDeps();
    delete deps.manifest.images;

    const response = await serve(
      imageRequest("https://app.example/_next/image?url=%2Fa.png&w=999&q=75"),
      deps,
    );

    expect(response.status).toBe(404);
    expect(await response.text()).toBe("<h1>not found</h1>");
  });

  it("serves both the basePath-prefixed and the bare path, as Next does", async () => {
    const deps = imageDeps();
    deps.manifest.basePath = "/docs";

    const unprefixed = await serve(
      imageRequest("https://app.example/_next/image?url=%2Fa.png&w=999&q=75"),
      deps,
    );
    expect(unprefixed.status).toBe(400);

    const prefixed = await serve(
      imageRequest("https://app.example/docs/_next/image?url=%2Fa.png&w=999&q=75"),
      deps,
    );
    expect(prefixed.status).toBe(400);
  });

  it("routes a valid request to the bound origin", async () => {
    const seen: ImageOriginRequest[] = [];
    const response = await serve(
      imageRequest("https://app.example/_next/image?url=%2Fa.png&w=640&q=75", {
        headers: { accept: "image/webp" },
      }),
      imageDeps({
        imageOrigin: async (payload) => {
          seen.push(payload);
          return new Response("optimized", { status: 200 });
        },
      }),
    );

    expect(response.status).toBe(200);
    expect(seen[0]).toMatchObject({
      assetPrefix: ASSET_PREFIX,
      url: "/a.png",
      w: 640,
      q: 75,
    });
  });

  it("serves a repeat request from the colo cache, across a redeploy", async () => {
    const clock = { ms: 0 };
    const pending: Promise<unknown>[] = [];
    const cache: CacheDeps = coloDeps({
      cache: caches.default,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
    });
    let calls = 0;
    const deps = imageDeps({
      cache,
      imageOrigin: async () => {
        calls++;
        return new Response("optimized", {
          status: 200,
          headers: { "cache-control": "public, max-age=60" },
        });
      },
    });
    deps.manifest.assetHashes = { "/a.png": "f".repeat(64) };
    const image = () =>
      serve(
        imageRequest("https://app.example/_next/image?url=%2Fa.png&w=640&q=75"),
        deps,
      );

    expect((await image()).headers.get("x-ocel-cache")).toBe("MISS");
    await Promise.all(pending.splice(0));

    clock.ms = 1_000;
    expect((await image()).headers.get("x-ocel-cache")).toBe("HIT");

    deps.manifest.buildId = "b2";
    const redeployed = await image();
    expect(redeployed.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await redeployed.text()).toBe("optimized");
    expect(calls).toBe(1);
  });

  it("never shares an unhashed variant between two deployments of one build", async () => {
    const clock = { ms: 0 };
    const pending: Promise<unknown>[] = [];
    const cache: CacheDeps = coloDeps({
      cache: caches.default,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
    });
    let calls = 0;
    const imageOrigin = async () => {
      calls++;
      return new Response("optimized", {
        status: 200,
        headers: { "cache-control": "public, max-age=60" },
      });
    };
    const image = (deps: RouteDeps) =>
      serve(
        imageRequest("https://app.example/_next/image?url=%2Fa.png&w=640&q=75"),
        deps,
      );

    const first = imageDeps({ cache, imageOrigin, deploymentId: "d1" });
    const second = imageDeps({ cache, imageOrigin, deploymentId: "d2" });
    expect(second.manifest.buildId).toBe(first.manifest.buildId);
    expect(first.manifest.assetHashes).toBeUndefined();

    expect((await image(first)).headers.get("x-ocel-cache")).toBe("MISS");
    await Promise.all(pending.splice(0));

    clock.ms = 1_000;
    expect((await image(first)).headers.get("x-ocel-cache")).toBe("HIT");
    expect((await image(second)).headers.get("x-ocel-cache")).toBe("MISS");
    expect(calls).toBe(2);
  });

  it("answers a validated request with 502 when no origin is provisioned", async () => {
    const response = await serve(
      imageRequest("https://app.example/_next/image?url=%2Fa.png&w=640&q=75"),
      imageDeps(),
    );

    expect(response.status).toBe(502);
  });

  it("is served without running middleware whose matcher covers it", async () => {
    let middlewareRan = 0;
    const deps = imageDeps({
      edge: async () => {
        middlewareRan++;
        return new Response(null, {
          status: 307,
          headers: { location: "/login", "x-middleware-next": "" },
        });
      },
    });
    deps.manifest.middleware = {
      entryKey: "middleware",
      matchers: [{ sourceRegex: "^(?:/((?!api).*))$" }],
    };

    const response = await serve(
      imageRequest("https://app.example/_next/image?url=%2Fa.png&w=999&q=75"),
      deps,
    );

    expect(middlewareRan).toBe(0);
    expect(response.status).toBe(400);
    expect(await response.text()).toBe('"w" parameter (width) of 999 is not allowed');
  });

  it("still runs that middleware for a path that is not an image request", async () => {
    let middlewareRan = 0;
    const deps = imageDeps({
      edge: async () => {
        middlewareRan++;
        return new Response(null, {
          status: 307,
          headers: { location: "/login", "x-middleware-next": "" },
        });
      },
    });
    deps.manifest.middleware = {
      entryKey: "middleware",
      matchers: [{ sourceRegex: "^(?:/((?!api).*))$" }],
    };

    await serve(imageRequest("https://app.example/dashboard"), deps);

    expect(middlewareRan).toBe(1);
  });
});
