import { describe, expect, it } from "vitest";

import type { CacheDeps } from "../src/cache";
import {
  IMAGE_PASSTHROUGH,
  unprovisionedImageOrigin,
  type ImageConfig,
} from "@framework/next-router/image";
import { serveImage, type ImageDeps } from "../src/image";
import fixtures from "@framework/next-router/fixtures/image-conformance.json";
import { coloDeps } from "./cache-deps";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]
  .config;

function testDeps(clock: { ms: number }): CacheDeps & { flush: () => Promise<void> } {
  const pending: Promise<unknown>[] = [];
  return {
    ...coloDeps({
      cache: caches.default,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
    }),
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
}

function recordingCache(): Cache & { keys: string[] } {
  const real = caches.default;
  const recording = {
    keys: [] as string[],
    match: (...args: Parameters<Cache["match"]>) => real.match(...args),
    put: (request: RequestInfo, response: Response) => {
      recording.keys.push(new Request(request).url);
      return real.put(request, response);
    },
    delete: (...args: Parameters<Cache["delete"]>) => real.delete(...args),
  };
  return recording as unknown as Cache & { keys: string[] };
}

function optimizer(body: string, init: ResponseInit = {}) {
  const fn = Object.assign(
    async () => {
      fn.calls++;
      return new Response(body, { status: 200, ...init });
    },
    { calls: 0 },
  );
  return fn;
}

function deps(
  overrides: Partial<ImageDeps> & { slug: string },
): ImageDeps {
  return {
    config: BASE_CONFIG,
    basePath: "",
    app: "web",
    deploymentId: "d1",
    origin: unprovisionedImageOrigin,
    ...overrides,
  };
}

const IMAGE = "url=%2Fa.png&w=640&q=75";

function request(search = IMAGE, init?: RequestInit): { url: URL; request: Request } {
  const url = new URL(`https://app.example/_next/image?${search}`);
  return { url, request: new Request(url, init) };
}

function get(imageDeps: ImageDeps, search = IMAGE, init?: RequestInit) {
  const { url, request: req } = request(search, init);
  return serveImage(req, url, imageDeps);
}

describe("the image colo tier", () => {
  it("misses, stores, and then hits within the ttl", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "hit", cache, origin });

    const miss = await get(d);
    expect(miss.headers.get("x-ocel-cache")).toBe("MISS");
    expect(miss.headers.get("x-nextjs-cache")).toBe("MISS");
    expect(await miss.text()).toBe("optimized");
    await cache.flush();

    clock.ms = 1_000;
    const hit = await get(d);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(hit.headers.get("x-nextjs-cache")).toBe("HIT");
    expect(await hit.text()).toBe("optimized");
    expect(origin.calls).toBe(1);
  });

  it("serves a stale entry immediately and refreshes behind the request", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    let body = "first";
    const origin = Object.assign(
      async () => {
        origin.calls++;
        return new Response(body, {
          status: 200,
          headers: { "cache-control": "public, max-age=60" },
        });
      },
      { calls: 0 },
    );
    const d = deps({ slug: "stale", cache, origin });

    await get(d);
    await cache.flush();

    body = "second";
    clock.ms = 20 * 60 * 60 * 1000; // past the 14400s the config floors the ttl at.
    const stale = await get(d);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(stale.headers.get("x-nextjs-cache")).toBe("STALE");
    expect(await stale.text()).toBe("first");
    expect(origin.calls).toBe(2); // the refresh, not this serve

    await cache.flush();
    const refreshed = await get(d);
    expect(refreshed.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await refreshed.text()).toBe("second");
  });

  it("leaves the entry intact when the refresh behind it 502s", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    let origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "stale-502", cache, origin: () => origin() });

    await get(d);
    await cache.flush();

    origin = Object.assign(
      async () => new Response("no optimizer", { status: 502 }),
      { calls: 0 },
    );
    clock.ms = 20 * 60 * 60 * 1000;
    const stale = await get(d);
    expect(stale.status).toBe(200);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await stale.text()).toBe("optimized");
    await cache.flush();

    const again = await get(d);
    expect(again.status).toBe(200);
    expect(again.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await again.text()).toBe("optimized");
  });

  it("never caches a 502", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    let calls = 0;
    const d = deps({
      slug: "no-502",
      cache,
      origin: async () => {
        calls++;
        return new Response("no optimizer", { status: 502 });
      },
    });

    const first = await get(d);
    expect(first.status).toBe(502);
    expect(first.headers.get("x-ocel-cache")).toBe("MISS");
    await cache.flush();

    clock.ms = 1_000;
    expect((await get(d)).status).toBe(502);
    expect(calls).toBe(2);
  });

  it("bypasses the cache when none is bound, and says so", async () => {
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "uncached", origin });

    const response = await get(d);
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(await response.text()).toBe("optimized");

    const again = await get(d);
    expect(again.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.calls).toBe(2);
  });

  it("answers a HEAD from the entry a GET would read, without a body", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "head-hit", cache, origin });

    await get(d);
    await cache.flush();

    clock.ms = 1_000;
    const head = await get(d, IMAGE, { method: "HEAD" });
    expect(head.headers.get("x-ocel-cache")).toBe("HIT");
    expect(head.headers.get("cache-control")).toBe(
      "public, max-age=14400, must-revalidate",
    );
    expect(head.body).toBeNull();
    expect(origin.calls).toBe(1);
  });

  it("populates the entry from a HEAD, so the following GET hits with a body", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "head-miss", cache, origin });

    const head = await get(d, IMAGE, { method: "HEAD" });
    expect(head.headers.get("x-ocel-cache")).toBe("MISS");
    expect(head.body).toBeNull();
    await cache.flush();

    clock.ms = 1_000;
    const hit = await get(d);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await hit.text()).toBe("optimized");
    expect(origin.calls).toBe(1);
  });

  it("bypasses a method no cache may answer", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "bypass-post", cache, origin });

    const response = await get(d, IMAGE, { method: "POST" });
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(origin.calls).toBe(1);

    await cache.flush();
    clock.ms = 1_000;
    expect((await get(d)).headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("treats an entry written in a format it does not know as a miss", async () => {
    const clock = { ms: 0 };
    const recording = recordingCache();
    const pending: Promise<unknown>[] = [];
    const cache: CacheDeps = coloDeps({
      cache: recording,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
    });
    const d = deps({
      slug: "version",
      cache,
      origin: optimizer("optimized", {
        headers: { "cache-control": "public, max-age=60" },
      }),
    });

    await get(d);
    await Promise.all(pending.splice(0));

    const key = new Request(recording.keys[0]);
    const stored = await caches.default.match(key);
    expect(stored?.headers.get("x-ocel-entry-version")).toBe("1");

    const headers = new Headers(stored!.headers);
    headers.set("x-ocel-entry-version", "99");
    await caches.default.put(key, new Response(stored!.body, { headers }));

    clock.ms = 1_000;
    expect((await get(d)).headers.get("x-ocel-cache")).toBe("MISS");
  });
});

describe("the image cache key", () => {
  it("misses when the compiled config changes", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });

    await get(deps({ slug: "confighash", cache, origin }));
    await cache.flush();

    clock.ms = 1_000;
    const tightened = await get(
      deps({
        slug: "confighash",
        cache,
        origin,
        config: { ...BASE_CONFIG, configHash: "0".repeat(64) },
      }),
    );
    expect(tightened.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });

  it("keeps a local image's entry across a redeploy that did not touch it", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const assetHashes = { "/a.png": "c0ffee".repeat(10) };

    await get(deps({ slug: "redeploy", cache, origin, assetHashes, deploymentId: "d1" }));
    await cache.flush();

    clock.ms = 1_000;
    const next = await get(
      deps({ slug: "redeploy", cache, origin, assetHashes, deploymentId: "d2" }),
    );
    expect(next.headers.get("x-ocel-cache")).toBe("HIT");
    expect(origin.calls).toBe(1);
  });

  it("misses when the source bytes changed", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });

    await get(
      deps({
        slug: "rehash",
        cache,
        origin,
        deploymentId: "d1",
        assetHashes: { "/a.png": "a".repeat(64) },
      }),
    );
    await cache.flush();

    clock.ms = 1_000;
    const changed = await get(
      deps({
        slug: "rehash",
        cache,
        origin,
        deploymentId: "d2",
        assetHashes: { "/a.png": "b".repeat(64) },
      }),
    );
    expect(changed.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });

  it("falls back to a deployment-scoped identity for a path the build never hashed", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });

    await get(deps({ slug: "nohash", cache, origin, deploymentId: "d1" }));
    await cache.flush();

    clock.ms = 1_000;
    const sameDeployment = await get(
      deps({ slug: "nohash", cache, origin, deploymentId: "d1" }),
    );
    expect(sameDeployment.headers.get("x-ocel-cache")).toBe("HIT");

    const redeployed = await get(deps({ slug: "nohash", cache, origin, deploymentId: "d2" }));
    expect(redeployed.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("keeps two apps of one project apart in the fallback", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });

    await get(deps({ slug: "twoapps", cache, origin, app: "web", deploymentId: "d1" }));
    await cache.flush();

    clock.ms = 1_000;
    const sameApp = await get(
      deps({ slug: "twoapps", cache, origin, app: "web", deploymentId: "d1" }),
    );
    expect(sameApp.headers.get("x-ocel-cache")).toBe("HIT");

    const otherApp = await get(
      deps({ slug: "twoapps", cache, origin, app: "admin", deploymentId: "d1" }),
    );
    expect(otherApp.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("strips the basePath the browser saw before consulting the hash map", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const assetHashes = { "/a.png": "d".repeat(64) };

    await get(
      deps({
        slug: "basepath",
        cache,
        origin,
        assetHashes,
        basePath: "/docs",
        deploymentId: "d1",
        config: { ...BASE_CONFIG, localPatterns: undefined },
      }),
      "url=%2Fdocs%2Fa.png&w=640&q=75",
    );
    await cache.flush();

    clock.ms = 1_000;
    const next = await get(
      deps({
        slug: "basepath",
        cache,
        origin,
        assetHashes,
        basePath: "/docs",
        deploymentId: "d2",
        config: { ...BASE_CONFIG, localPatterns: undefined },
      }),
      "url=%2Fdocs%2Fa.png&w=640&q=75",
    );
    expect(next.headers.get("x-ocel-cache")).toBe("HIT");
  });

  it("keys a remote image on its normalized absolute url", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "remote", cache, origin, deploymentId: "d1" });
    const src = (path: string) =>
      `url=${encodeURIComponent(`https://cdn.allowed.example${path}`)}&w=640&q=75`;

    await get(d, src("/img/a.png"));
    await cache.flush();

    clock.ms = 1_000;
    const normalized = await get(
      deps({ slug: "remote", cache, origin, deploymentId: "d2" }),
      src("/img/../img/a.png"),
    );
    expect(normalized.headers.get("x-ocel-cache")).toBe("HIT");

    const other = await get(d, src("/img/b.png"));
    expect(other.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("keys on the negotiated type rather than the Accept header", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "accept", cache, origin });

    await get(d, IMAGE, { headers: { accept: "image/webp,image/apng,*/*;q=0.8" } });
    await cache.flush();

    clock.ms = 1_000;
    const other = await get(d, IMAGE, {
      headers: { accept: "text/html,image/webp,*/*;q=0.5" },
    });
    expect(other.headers.get("x-ocel-cache")).toBe("HIT");
    expect(origin.calls).toBe(1);

    const wildcard = await get(d, IMAGE, { headers: { accept: "*/*" } });
    expect(wildcard.headers.get("x-ocel-cache")).toBe("MISS");
  });

  it("keys on the width and the quality", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });
    const d = deps({ slug: "dimensions", cache, origin });

    await get(d, "url=%2Fa.png&w=640&q=75");
    await cache.flush();

    clock.ms = 1_000;
    expect((await get(d, "url=%2Fa.png&w=750&q=75")).headers.get("x-ocel-cache")).toBe(
      "MISS",
    );
  });
});

describe("the ttl the browser is told", () => {
  const upstream = (cacheControl: string, extra: Record<string, string> = {}) =>
    optimizer("optimized", { headers: { "cache-control": cacheControl, ...extra } });

  async function cacheControlFor(
    slug: string,
    origin: ReturnType<typeof optimizer>,
    overrides: Partial<ImageDeps> = {},
    search = IMAGE,
  ): Promise<string | null> {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const response = await get(deps({ slug, cache, origin, ...overrides }), search);
    await cache.flush();
    return response.headers.get("cache-control");
  }

  it("floors the upstream max-age at the configured minimum", async () => {
    expect(await cacheControlFor("ttl-floor", upstream("public, max-age=60"))).toBe(
      "public, max-age=14400, must-revalidate",
    );
  });

  it("takes an upstream max-age above the minimum", async () => {
    expect(await cacheControlFor("ttl-above", upstream("public, max-age=86400"))).toBe(
      "public, max-age=86400, must-revalidate",
    );
  });

  it("reads s-maxage ahead of max-age", async () => {
    expect(
      await cacheControlFor("ttl-smaxage", upstream("s-maxage=86400, max-age=60")),
    ).toBe("public, max-age=86400, must-revalidate");
  });

  it("never consults Expires", async () => {
    expect(
      await cacheControlFor(
        "ttl-expires",
        upstream("public, max-age=60", {
          expires: new Date(Date.now() + 86400_000).toUTCString(),
        }),
      ),
    ).toBe("public, max-age=14400, must-revalidate");
  });

  it("uses the minimum when the upstream said nothing", async () => {
    expect(
      await cacheControlFor("ttl-none", optimizer("optimized")),
    ).toBe("public, max-age=14400, must-revalidate");
  });

  it("ignores the upstream entirely on the optimization-failure passthrough", async () => {
    expect(
      await cacheControlFor(
        "ttl-passthrough",
        upstream("public, max-age=86400", { [IMAGE_PASSTHROUGH]: "sharp" }),
      ),
    ).toBe("public, max-age=14400, must-revalidate");
  });

  it("never lets the passthrough marker reach the entry or the browser", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = upstream("public, max-age=86400", { [IMAGE_PASSTHROUGH]: "sharp" });
    const d = deps({ slug: "passthrough-strip", cache, origin });

    const miss = await get(d);
    expect(miss.headers.get(IMAGE_PASSTHROUGH)).toBeNull();
    await cache.flush();

    clock.ms = 1_000;
    const hit = await get(d);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(hit.headers.get(IMAGE_PASSTHROUGH)).toBeNull();
  });

  it("makes a static import immutable", async () => {
    for (const [name, prefix] of [
      ["media", "/_next/static/media"],
      ["immutable", "/_next/static/immutable/media"],
    ]) {
      expect(
        await cacheControlFor(
          `ttl-static-${name}`,
          upstream("public, max-age=60"),
          { config: { ...BASE_CONFIG, localPatterns: undefined } },
          `url=${encodeURIComponent(`${prefix}/logo.abc123.png`)}&w=640&q=75`,
        ),
      ).toBe("public, max-age=315360000, immutable");
    }
  });

  it("keeps the derived window on a hit rather than restating the entry's", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = upstream("public, max-age=86400");
    const d = deps({ slug: "ttl-hit", cache, origin });

    await get(d);
    await cache.flush();

    clock.ms = 1_000;
    const hit = await get(d);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(hit.headers.get("cache-control")).toBe(
      "public, max-age=86400, must-revalidate",
    );
    expect(hit.headers.get("vary")).toBe("accept");
  });

  it("declines to store an image nothing may keep", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = upstream("public, max-age=0");
    const d = deps({
      slug: "ttl-zero",
      cache,
      origin,
      config: { ...BASE_CONFIG, minimumCacheTTL: 0 },
    });

    const first = await get(d);
    expect(first.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
    await cache.flush();

    expect((await get(d)).headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });
});

describe("a rejected request", () => {
  it("carries none of the served-image headers", async () => {
    const clock = { ms: 0 };
    const response = await get(
      deps({ slug: "rejected", cache: testDeps(clock) }),
      "url=%2Fa.png&w=999&q=75",
    );

    expect(response.status).toBe(400);
    for (const header of ["vary", "cache-control", "content-type"]) {
      expect(response.headers.get(header)).toBeNull();
    }
  });

  it("still reports the tier that answered it", async () => {
    const clock = { ms: 0 };
    const d = deps({ slug: "rejected-status", cache: testDeps(clock) });

    const bad = await get(d, "url=%2Fa.png&w=999&q=75");
    expect(bad.status).toBe(400);
    expect(bad.headers.get("x-ocel-cache")).toBe("BYPASS");

    const unparseable = await get(d, "url=%2F%25&w=640&q=75");
    expect(unparseable.status).toBe(500);
    expect(unparseable.headers.get("x-ocel-cache")).toBe("BYPASS");
  });
});

describe("what the image cache key does and does not collapse", () => {
  const origin = () =>
    optimizer("optimized", { headers: { "cache-control": "public, max-age=60" } });

  it("shares one entry between a public/ image and its static-import twin, with different ttls", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const bytes = "e".repeat(64);
    const d = deps({
      slug: "twin",
      cache,
      origin: o,
      config: { ...BASE_CONFIG, localPatterns: undefined },
      assetHashes: { "/logo.png": bytes, "/_next/static/media/logo.abc123.png": bytes },
    });
    const src = (path: string) => `url=${encodeURIComponent(path)}&w=640&q=75`;

    const staticImport = await get(d, src("/_next/static/media/logo.abc123.png"));
    expect(staticImport.headers.get("x-ocel-cache")).toBe("MISS");
    expect(staticImport.headers.get("cache-control")).toBe(
      "public, max-age=315360000, immutable",
    );
    await cache.flush();

    clock.ms = 1_000;
    const fromPublic = await get(d, src("/logo.png"));
    expect(fromPublic.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await fromPublic.text()).toBe("optimized");
    expect(fromPublic.headers.get("cache-control")).toBe(
      "public, max-age=14400, must-revalidate",
    );
    expect(o.calls).toBe(1);
  });

  it("keeps the immutable claim on a hit written by the mutable twin", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const bytes = "f".repeat(64);
    const d = deps({
      slug: "twin-reverse",
      cache,
      origin: o,
      config: { ...BASE_CONFIG, localPatterns: undefined },
      assetHashes: { "/logo.png": bytes, "/_next/static/media/logo.abc123.png": bytes },
    });
    const src = (path: string) => `url=${encodeURIComponent(path)}&w=640&q=75`;

    await get(d, src("/logo.png"));
    await cache.flush();

    clock.ms = 1_000;
    const staticImport = await get(d, src("/_next/static/media/logo.abc123.png"));
    expect(staticImport.headers.get("x-ocel-cache")).toBe("HIT");
    expect(staticImport.headers.get("cache-control")).toBe(
      "public, max-age=315360000, immutable",
    );
    expect(o.calls).toBe(1);
  });

  it("gives genuinely different sources genuinely different entries", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const config = { ...BASE_CONFIG, localPatterns: undefined };
    const src = (path: string) => `url=${encodeURIComponent(path)}&w=640&q=75`;

    const cases: Array<[Partial<ImageDeps> & { slug: string }, string]> = [
      [{ slug: "distinct", config }, src("/a.png")],
      [{ slug: "distinct", config }, src("/b.png")],
      [{ slug: "distinct", config }, src("/nested/a.png")],
      [{ slug: "distinct-other-project", config }, src("/a.png")],
      [{ slug: "distinct", config }, src("https://cdn.allowed.example/img/a.png")],
      [{ slug: "distinct", config }, src("https://cdn2.allowed.example/img/a.png")],
    ];

    for (const [overrides, search] of cases) {
      const response = await get(deps({ cache, origin: o, ...overrides }), search);
      expect([search, response.headers.get("x-ocel-cache")]).toEqual([search, "MISS"]);
      await cache.flush();
      clock.ms += 1_000;
    }
    expect(o.calls).toBe(cases.length);
  });

  it("normalizes the fallback identity the way the hash-map branch does", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const d = deps({
      slug: "fallback-normal",
      cache,
      origin: o,
      deploymentId: "d1",
      config: { ...BASE_CONFIG, localPatterns: undefined },
    });
    const src = (path: string) => `url=${encodeURIComponent(path)}&w=640&q=75`;

    await get(d, src("/a.png"));
    await cache.flush();

    clock.ms = 1_000;
    for (const equivalent of ["/x/../a.png", "/x/y/../../a.png", "/./a.png"]) {
      const response = await get(d, src(equivalent));
      expect([equivalent, response.headers.get("x-ocel-cache")]).toEqual([
        equivalent,
        "HIT",
      ]);
    }
    expect(o.calls).toBe(1);
  });

  it("keeps two queries on one path apart in the fallback", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const d = deps({
      slug: "fallback-query",
      cache,
      origin: o,
      config: { ...BASE_CONFIG, localPatterns: [{ pathname: "^/.*$" }] },
    });
    const src = (path: string) => `url=${encodeURIComponent(path)}&w=640&q=75`;

    await get(d, src("/img?id=1"));
    await cache.flush();

    clock.ms = 1_000;
    expect((await get(d, src("/img?id=2"))).headers.get("x-ocel-cache")).toBe("MISS");
    expect(o.calls).toBe(2);
  });

  it("does not let a bare path answer from the basePath-prefixed entry", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const d = deps({
      slug: "basepath-alias",
      cache,
      origin: o,
      basePath: "/docs",
      deploymentId: "d1",
      assetHashes: { "/a.png": "a".repeat(64) },
      config: { ...BASE_CONFIG, localPatterns: undefined },
    });

    await get(d, "url=%2Fdocs%2Fa.png&w=640&q=75");
    await cache.flush();

    clock.ms = 1_000;
    expect((await get(d, "url=%2Fa.png&w=640&q=75")).headers.get("x-ocel-cache")).toBe(
      "MISS",
    );
    expect(o.calls).toBe(2);
  });
});
