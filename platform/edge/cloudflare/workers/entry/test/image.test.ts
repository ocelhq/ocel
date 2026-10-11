import type { AssetBucket } from "@framework/next-router/assets";
import fixtures from "@framework/next-router/fixtures/image-conformance.json";
import type { ImageConfig } from "@framework/next-router/image";
import { describe, expect, it } from "vitest";
import { type RouteDeps, serve } from "../src/index";
import { IMAGE_CALL_HEADER, RENDER_RELEASE_HEADER, readImageOptimizer } from "../src/serve-key";
import { fakeServing } from "./serving-deps";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0].config;

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
    appBuildId: "d1",
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

describe("the /_next/image route through Workers Cache", () => {
  const OPTIMIZER = "https://opt.lambda-url.eu-west-2.on.aws/";
  const IMAGE = "https://app.example/_next/image?url=%2Fa.png&w=640&q=75";
  const optimized = (cacheStatus = "MISS") =>
    new Response("optimized", {
      status: 200,
      headers: {
        "content-type": "image/webp",
        "cache-control": "public, max-age=14400, must-revalidate",
        "cache-tag": "r0a1b2c3d,r0a1b2c3d|path:/_next/image",
        "cloudflare-cdn-cache-control": "max-age=14400",
        "cf-cache-status": cacheStatus,
      },
    });

  function cachedImageDeps(answer: () => Response | Promise<Response> = optimized) {
    const fake = fakeServing(answer);
    let originCalls = 0;
    const deps = imageDeps({
      serving: fake.serving,
      imageOptimizerUrl: OPTIMIZER,
      imageOrigin: async () => {
        originCalls++;
        return new Response("from the optimizer", {
          status: 200,
          headers: { "content-type": "image/webp" },
        });
      },
    });
    return { fake, deps, originCalls: () => originCalls };
  }

  it("asks Serve for the image under the release, keyed by the image's digest", async () => {
    const { fake, deps } = cachedImageDeps();

    await serve(imageRequest(IMAGE, { headers: { accept: "image/webp" } }), deps);

    expect(fake.calls).toHaveLength(1);
    const [{ request, props }] = fake.calls;
    expect(props).toEqual({ kind: "origin" });
    expect(request.method).toBe("GET");
    expect(readImageOptimizer(request.url)).toBe(OPTIMIZER);
    expect(new URL(request.url).pathname).toMatch(/\/[0-9a-f]{64}$/);
    expect(JSON.parse(request.headers.get(RENDER_RELEASE_HEADER)!)).toMatchObject({
      slug: "p1",
      isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
    });
    expect(JSON.parse(request.headers.get(IMAGE_CALL_HEADER)!)).toEqual({
      payload: {
        assetPrefix: ASSET_PREFIX,
        url: "/a.png",
        w: 640,
        q: 75,
        accept: "image/webp",
        mimeType: "image/webp",
        configHash: BASE_CONFIG.configHash,
      },
      minimumCacheTTL: BASE_CONFIG.minimumCacheTTL,
    });
  });

  it("gives the visitor the image without the edge's cache headers", async () => {
    const { deps } = cachedImageDeps();

    const response = await serve(imageRequest(IMAGE), deps);

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("optimized");
    expect(response.headers.get("cache-control")).toBe("public, max-age=14400, must-revalidate");
    expect(response.headers.get("vary")).toBe("accept");
    for (const name of ["cache-tag", "cloudflare-cdn-cache-control", "cf-cache-status"]) {
      expect(response.headers.has(name)).toBe(false);
    }
  });

  it("reports the tier Workers Cache answered from", async () => {
    for (const [cfStatus, expected] of [
      ["HIT", "HIT"],
      ["REVALIDATED", "HIT"],
      ["STALE", "STALE"],
      ["MISS", "MISS"],
      ["EXPIRED", "MISS"],
    ] as const) {
      const { deps } = cachedImageDeps(() => optimized(cfStatus));

      const response = await serve(imageRequest(IMAGE), deps);

      expect(response.headers.get("x-ocel-cache")).toBe(expected);
    }
  });

  it("tells Next's cache status from the tier Workers Cache answered from", async () => {
    for (const [cfStatus, expected] of [
      ["HIT", "HIT"],
      ["STALE", "STALE"],
      ["MISS", "MISS"],
    ] as const) {
      const { deps } = cachedImageDeps(() => {
        const stored = optimized(cfStatus);
        stored.headers.set("x-nextjs-cache", "MISS");
        return stored;
      });

      const response = await serve(imageRequest(IMAGE), deps);

      expect(response.headers.get("x-nextjs-cache")).toBe(expected);
    }
  });

  it("asks Serve for an image whose path is not ASCII", async () => {
    const { fake, deps } = cachedImageDeps();

    const response = await serve(
      imageRequest("https://app.example/_next/image?url=%2F%E5%9B%BE%E7%89%87.png&w=640&q=75"),
      deps,
    );

    expect(response.status).toBe(200);
    expect(fake.calls).toHaveLength(1);
    const header = fake.calls[0].request.headers.get(IMAGE_CALL_HEADER)!;
    expect(header).toMatch(/^[\x20-\x7e]*$/);
    expect(JSON.parse(header).payload.url).toBe("/图片.png");
  });

  it("serves a static import as immutable whatever lifetime Workers Cache keeps it for", async () => {
    const { deps } = cachedImageDeps();

    const response = await serve(
      imageRequest(
        `https://app.example/_next/image?url=${encodeURIComponent("/_next/static/media/logo.abc123.png")}&w=640&q=75`,
      ),
      deps,
    );

    expect(response.headers.get("cache-control")).toBe("public, max-age=315360000, immutable");
  });

  it("answers a HEAD without a body", async () => {
    const { deps } = cachedImageDeps();

    const response = await serve(imageRequest(IMAGE, { method: "HEAD" }), deps);

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("");
  });

  it("renders at the optimizer, uncached, when Serve refuses the call", async () => {
    const { deps, originCalls } = cachedImageDeps(
      () => new Response("refused", { status: 503, headers: { "x-ocel-serve-refused": "1" } }),
    );

    const response = await serve(imageRequest(IMAGE), deps);

    expect(await response.text()).toBe("from the optimizer");
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(originCalls()).toBe(1);
  });

  it("does not ask Serve for a method that is not a read", async () => {
    const { fake, deps, originCalls } = cachedImageDeps();

    const response = await serve(imageRequest(IMAGE, { method: "POST" }), deps);

    expect(fake.calls).toHaveLength(0);
    expect(originCalls()).toBe(1);
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("does not ask Serve when no optimizer address is known to name in the call", async () => {
    const { fake, deps, originCalls } = cachedImageDeps();
    deps.imageOptimizerUrl = undefined;

    const response = await serve(imageRequest(IMAGE), deps);

    expect(fake.calls).toHaveLength(0);
    expect(originCalls()).toBe(1);
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
  });
});
