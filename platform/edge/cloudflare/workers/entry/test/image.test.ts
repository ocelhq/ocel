import fixtures from "@framework/next-router/fixtures/image-conformance.json";
import type { ImageConfig, ImageOriginRequest } from "@framework/next-router/image";
import { describe, expect, it } from "vitest";
import { IMAGE_CALL_HEADER, RENDER_RELEASE_HEADER, readImageOptimizer } from "../src/serve-key";
import { countingLoader, edgeBundle } from "./edge-bundle";
import {
  ISR_PREFIX,
  type NextRelease,
  servedNextRelease,
  UNTOKENED_ISR_PREFIX,
} from "./next-release";
import { fakeServing } from "./serving-deps";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0].config;

const ASSET_PREFIX = "prod/p1/web/r3f8a1c9d/assets";

function imageRelease(over: NextRelease = {}) {
  return servedNextRelease({
    ...over,
    table: { buildId: "b1", images: BASE_CONFIG, ...over.table },
    record: { assetPrefix: ASSET_PREFIX, isrPrefix: UNTOKENED_ISR_PREFIX, ...over.record },
    assets: { "/404.html": "<h1>not found</h1>" },
  });
}

const REDIRECTING_MIDDLEWARE = edgeBundle({
  middleware: `async () => new Response(null, {
     status: 307,
     headers: { location: "/login", "x-middleware-next": "" },
   })`,
});

const MIDDLEWARE = { entryKey: "middleware", matchers: [{ sourceRegex: "^(?:/((?!api).*))$" }] };

const IMAGE = "https://app.example/_next/image?url=%2Fa.png&w=640&q=75";

const OVERSIZED = "https://app.example/_next/image?url=%2Fa.png&w=999&q=75";

describe("the /_next/image route", () => {
  it("is handled ahead of the static-asset fallthrough", async () => {
    const response = await imageRelease()(new Request(OVERSIZED));

    expect(response.status).toBe(400);
    expect(await response.text()).toBe('"w" parameter (width) of 999 is not allowed');
  });

  it("is not registered when the build emitted no image config", async () => {
    const response = await imageRelease({ table: { images: undefined } })(new Request(OVERSIZED));

    expect(response.status).toBe(404);
    expect(await response.text()).toBe("<h1>not found</h1>");
  });

  it("serves both the basePath-prefixed and the bare path, as Next does", async () => {
    const serve = imageRelease({ table: { basePath: "/docs" } });

    const unprefixed = await serve(new Request(OVERSIZED));
    expect(unprefixed.status).toBe(400);

    const prefixed = await serve(
      new Request("https://app.example/docs/_next/image?url=%2Fa.png&w=999&q=75"),
    );
    expect(prefixed.status).toBe(400);
  });

  it("routes a valid request to the bound origin", async () => {
    const seen: ImageOriginRequest[] = [];
    const serve = imageRelease({
      base: {
        imageOrigin: async (payload) => {
          seen.push(payload);
          return new Response("optimized", { status: 200 });
        },
      },
    });

    const response = await serve(new Request(IMAGE, { headers: { accept: "image/webp" } }));

    expect(response.status).toBe(200);
    expect(seen[0]).toMatchObject({
      assetPrefix: ASSET_PREFIX,
      url: "/a.png",
      w: 640,
      q: 75,
    });
  });

  it("answers a validated request with 502 when no origin is provisioned", async () => {
    const response = await imageRelease()(new Request(IMAGE));

    expect(response.status).toBe(502);
  });

  it("is served without running middleware whose matcher covers it", async () => {
    const { loader, invocations } = countingLoader();
    const serve = imageRelease({
      table: { middleware: MIDDLEWARE },
      edgeBundle: REDIRECTING_MIDDLEWARE,
      loader,
    });

    const response = await serve(new Request(OVERSIZED));

    expect(invocations).toHaveLength(0);
    expect(response.status).toBe(400);
    expect(await response.text()).toBe('"w" parameter (width) of 999 is not allowed');
  });

  it("still runs that middleware for a path that is not an image request", async () => {
    const { loader, invocations } = countingLoader();
    const serve = imageRelease({
      table: { middleware: MIDDLEWARE },
      edgeBundle: REDIRECTING_MIDDLEWARE,
      loader,
    });

    await serve(new Request("https://app.example/dashboard"));

    expect(invocations).toHaveLength(1);
  });
});

describe("the /_next/image route through Workers Cache", () => {
  const OPTIMIZER = "https://opt.lambda-url.eu-west-2.on.aws/";
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

  function cachedImage(
    answer: () => Response | Promise<Response> = optimized,
    optimizerUrl: string | null = OPTIMIZER,
  ) {
    const fake = fakeServing(answer);
    let originCalls = 0;
    const serve = imageRelease({
      record: { isrPrefix: ISR_PREFIX },
      serve: fake.serving.serve,
      base: {
        imageOptimizerUrl: optimizerUrl ?? undefined,
        imageOrigin: async () => {
          originCalls++;
          return new Response("from the optimizer", {
            status: 200,
            headers: { "content-type": "image/webp" },
          });
        },
      },
    });
    return { fake, serve, originCalls: () => originCalls };
  }

  it("asks Serve for the image under the release, keyed by the image's digest", async () => {
    const { fake, serve } = cachedImage();

    await serve(new Request(IMAGE, { headers: { accept: "image/webp" } }));

    expect(fake.calls).toHaveLength(1);
    const [{ request, props }] = fake.calls;
    expect(props).toEqual({ kind: "origin" });
    expect(request.method).toBe("GET");
    expect(readImageOptimizer(request.url)).toBe(OPTIMIZER);
    expect(new URL(request.url).pathname).toMatch(/\/[0-9a-f]{64}$/);
    expect(JSON.parse(request.headers.get(RENDER_RELEASE_HEADER)!)).toMatchObject({
      slug: "p1",
      isrPrefix: ISR_PREFIX,
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
    const { serve } = cachedImage();

    const response = await serve(new Request(IMAGE));

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
      const { serve } = cachedImage(() => optimized(cfStatus));

      const response = await serve(new Request(IMAGE));

      expect(response.headers.get("x-ocel-cache")).toBe(expected);
    }
  });

  it("tells Next's cache status from the tier Workers Cache answered from", async () => {
    for (const [cfStatus, expected] of [
      ["HIT", "HIT"],
      ["STALE", "STALE"],
      ["MISS", "MISS"],
    ] as const) {
      const { serve } = cachedImage(() => {
        const stored = optimized(cfStatus);
        stored.headers.set("x-nextjs-cache", "MISS");
        return stored;
      });

      const response = await serve(new Request(IMAGE));

      expect(response.headers.get("x-nextjs-cache")).toBe(expected);
    }
  });

  it("asks Serve for an image whose path is not ASCII", async () => {
    const { fake, serve } = cachedImage();

    const response = await serve(
      new Request("https://app.example/_next/image?url=%2F%E5%9B%BE%E7%89%87.png&w=640&q=75"),
    );

    expect(response.status).toBe(200);
    expect(fake.calls).toHaveLength(1);
    const header = fake.calls[0].request.headers.get(IMAGE_CALL_HEADER)!;
    expect(header).toMatch(/^[\x20-\x7e]*$/);
    expect(JSON.parse(header).payload.url).toBe("/图片.png");
  });

  it("serves a static import as immutable whatever lifetime Workers Cache keeps it for", async () => {
    const { serve } = cachedImage();

    const response = await serve(
      new Request(
        `https://app.example/_next/image?url=${encodeURIComponent("/_next/static/media/logo.abc123.png")}&w=640&q=75`,
      ),
    );

    expect(response.headers.get("cache-control")).toBe("public, max-age=315360000, immutable");
  });

  it("answers a HEAD without a body", async () => {
    const { serve } = cachedImage();

    const response = await serve(new Request(IMAGE, { method: "HEAD" }));

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("");
  });

  it("renders at the optimizer, uncached, when Serve refuses the call", async () => {
    const { serve, originCalls } = cachedImage(
      () => new Response("refused", { status: 503, headers: { "x-ocel-serve-refused": "1" } }),
    );

    const response = await serve(new Request(IMAGE));

    expect(await response.text()).toBe("from the optimizer");
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(originCalls()).toBe(1);
  });

  it("does not ask Serve for a method that is not a read", async () => {
    const { fake, serve, originCalls } = cachedImage();

    const response = await serve(new Request(IMAGE, { method: "POST" }));

    expect(fake.calls).toHaveLength(0);
    expect(originCalls()).toBe(1);
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("does not ask Serve when no optimizer address is known to name in the call", async () => {
    const { fake, serve, originCalls } = cachedImage(optimized, null);

    const response = await serve(new Request(IMAGE));

    expect(fake.calls).toHaveLength(0);
    expect(originCalls()).toBe(1);
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
  });

  it("keys a relative image by the build its release names", async () => {
    const keyed = async (buildId: string) => {
      const fake = fakeServing(optimized);
      await imageRelease({
        record: { isrPrefix: ISR_PREFIX, buildId },
        serve: fake.serving.serve,
        base: { imageOptimizerUrl: OPTIMIZER },
      })(new Request(IMAGE));
      return fake.calls[0].request.url;
    };

    expect(await keyed("d1")).toBe(await keyed("d1"));
    expect(await keyed("d1")).not.toBe(await keyed("d2"));
  });
});
