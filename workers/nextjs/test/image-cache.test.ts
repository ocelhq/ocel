// The colo tier as it carries an optimized image: the cache key's identity, the
// TTL the browser is told, and the HIT/STALE/MISS transitions. caches.default
// is a no-op on *.workers.dev, so none of this is observable on a deployment
// there — every test here drives the machinery directly.
import { describe, expect, it } from "vitest";

import type { CacheDeps } from "../src/cache";
import {
  IMAGE_PASSTHROUGH,
  serveImage,
  unprovisionedImageOrigin,
  type ImageConfig,
  type ImageDeps,
} from "../src/image";
import fixtures from "./fixtures/image-conformance.json";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]
  .config;

// A CacheDeps over the real workerd cache with a manual clock and a waitUntil
// the test flushes by hand, so a background refresh is observed rather than
// raced. Every test names its own slug: the cache is process-wide and the key
// is a hash over the slug, so that is what keeps one test's entries out of
// another's.
function testDeps(clock: { ms: number }): CacheDeps & { flush: () => Promise<void> } {
  const pending: Promise<unknown>[] = [];
  return {
    cache: caches.default,
    now: () => clock.ms,
    waitUntil: (promise) => {
      pending.push(promise);
    },
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
}

// Delegates to the real cache while recording the keys written, so a test can
// reach an entry the worker addressed without reimplementing the key.
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

// An origin counting its calls, standing in for the optimizer PR 5 lands.
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
    buildId: "b1",
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

  // The whole point of a background refresh: the stale bytes go out on this
  // request, and the optimization runs behind it.
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

  // A failing refresh must leave the entry alone rather than replace it with the
  // failure. The discriminating read is the second one, after the refresh has
  // run: had the 502 been stored, it would answer 502 there, and only an entry
  // the store refused leaves the optimizer's own bytes to serve.
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

    // The refresh has now run and failed. The entry is what it was: still
    // present, still stale, still the bytes the optimizer last produced.
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

  // HEAD is safe and cacheable, and an optimization carries none of the
  // per-visitor semantics that make a prerender bypass on it. Bypassing would
  // put a full optimizer invocation on the bill for every one of them.
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

  // ...and one that misses populates the entry the same way, rather than
  // leaving a bodiless response behind for the next GET to read.
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

    // Nothing was written, so a GET behind it still misses.
    await cache.flush();
    clock.ms = 1_000;
    expect((await get(d)).headers.get("x-ocel-cache")).toBe("MISS");
  });

  // The version is what lets PR 6's durable tier change the entry format
  // without a key change and without a flush: an entry this worker cannot read
  // is re-optimized, not misread.
  it("treats an entry written in a format it does not know as a miss", async () => {
    const clock = { ms: 0 };
    const recording = recordingCache();
    const pending: Promise<unknown>[] = [];
    const cache: CacheDeps = {
      cache: recording,
      now: () => clock.ms,
      waitUntil: (promise) => {
        pending.push(promise);
      },
    };
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
    // A tightened remotePatterns republishes the config under a new hash. An
    // entry admitted under the old one must be unreachable, not merely stale.
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

    await get(deps({ slug: "redeploy", cache, origin, assetHashes, buildId: "b1" }));
    await cache.flush();

    clock.ms = 1_000;
    const next = await get(
      deps({ slug: "redeploy", cache, origin, assetHashes, buildId: "b2" }),
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
        buildId: "b1",
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
        buildId: "b2",
        assetHashes: { "/a.png": "b".repeat(64) },
      }),
    );
    expect(changed.headers.get("x-ocel-cache")).toBe("MISS");
    expect(origin.calls).toBe(2);
  });

  // Without a hash there is nothing content-addressable to key on, so the entry
  // is scoped to the build that produced it: correct, but flushed by a redeploy.
  it("falls back to a build-scoped identity for a path the build never hashed", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const origin = optimizer("optimized", {
      headers: { "cache-control": "public, max-age=60" },
    });

    await get(deps({ slug: "nohash", cache, origin, buildId: "b1" }));
    await cache.flush();

    clock.ms = 1_000;
    const sameBuild = await get(deps({ slug: "nohash", cache, origin, buildId: "b1" }));
    expect(sameBuild.headers.get("x-ocel-cache")).toBe("HIT");

    const redeployed = await get(deps({ slug: "nohash", cache, origin, buildId: "b2" }));
    expect(redeployed.headers.get("x-ocel-cache")).toBe("MISS");
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
        buildId: "b1",
        config: { ...BASE_CONFIG, localPatterns: undefined },
      }),
      "url=%2Fdocs%2Fa.png&w=640&q=75",
    );
    await cache.flush();

    // The same file, addressed by the same hash, under a later build.
    clock.ms = 1_000;
    const next = await get(
      deps({
        slug: "basepath",
        cache,
        origin,
        assetHashes,
        basePath: "/docs",
        buildId: "b2",
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
    const d = deps({ slug: "remote", cache, origin, buildId: "b1" });
    const src = (path: string) =>
      `url=${encodeURIComponent(`https://cdn.allowed.example${path}`)}&w=640&q=75`;

    await get(d, src("/img/a.png"));
    await cache.flush();

    clock.ms = 1_000;
    // The same url written the long way round, and a different build.
    const normalized = await get(
      deps({ slug: "remote", cache, origin, buildId: "b2" }),
      src("/img/../img/a.png"),
    );
    expect(normalized.headers.get("x-ocel-cache")).toBe("HIT");

    const other = await get(d, src("/img/b.png"));
    expect(other.headers.get("x-ocel-cache")).toBe("MISS");
  });

  // The negotiated type, never the raw header: two browsers whose Accept
  // strings differ in every other way still describe the same output bytes.
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

    // A header that negotiates nothing describes different bytes — the source
    // format, not webp — and is a different entry.
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

  // The bytes the transform failed on. Next holds those for the configured
  // minimum whatever the upstream would have allowed.
  it("ignores the upstream entirely on the optimization-failure passthrough", async () => {
    expect(
      await cacheControlFor(
        "ttl-passthrough",
        upstream("public, max-age=86400", { [IMAGE_PASSTHROUGH]: "sharp" }),
      ),
    ).toBe("public, max-age=14400, must-revalidate");
  });

  // The optimizer's own signal, read for the ttl and dropped there. Advertising
  // to every client that a transform failed is the deployment's business.
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

// The 400s and 500s stay bare: no Vary, no Content-Type, no Cache-Control.
// Next's own do, and the conformance fixtures record it. x-ocel-cache is the one
// exception — it is Ocel's diagnostic, no fixture pins it, and an operator
// reading a 400 would otherwise get no tier signal where a 502 gives one.
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

    // The 500 path — a url no runtime can parse — answers the same way.
    const unparseable = await get(d, "url=%2F%25&w=640&q=75");
    expect(unparseable.status).toBe(500);
    expect(unparseable.headers.get("x-ocel-cache")).toBe("BYPASS");
  });
});

// Every other key test varies one input and asserts a MISS, which cannot catch
// two genuinely different requests landing on one entry. These assert the
// converse: what must not collide, and what must.
describe("what the image cache key does and does not collapse", () => {
  const origin = () =>
    optimizer("optimized", { headers: { "cache-control": "public, max-age=60" } });

  // The finding this whole class of test exists for. public/logo.png and the
  // static-import copy of the same file are identical bytes, hash identically,
  // and share one entry — no fragmentation, no second optimizer invocation —
  // but only the content-hashed url may be called immutable.
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
    // The entry was written by the immutable request and must not have pinned
    // this one for ten years: a public/ path has no purge story.
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

  // Distinct files under one build, distinct remote hosts, and one path served
  // through two different projects: none of these are the same optimized image,
  // and no pair of them may answer for another.
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

  // The hash-map branch normalizes through new URL; the fallback must agree, or
  // the same file requested three equivalent ways is three optimizer calls.
  it("normalizes the fallback identity the way the hash-map branch does", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const d = deps({
      slug: "fallback-normal",
      cache,
      origin: o,
      buildId: "b1",
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

  // A query survives normalization: a local route is free to serve different
  // bytes per query, and unlike a path it names no file to hash.
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

  // Under a basePath, /a.png names no file this deployment serves. It validates
  // (Next strips no basePath from the url parameter), so it must miss on its own
  // identity rather than answer from /docs/a.png's entry.
  it("does not let a bare path answer from the basePath-prefixed entry", async () => {
    const clock = { ms: 0 };
    const cache = testDeps(clock);
    const o = origin();
    const d = deps({
      slug: "basepath-alias",
      cache,
      origin: o,
      basePath: "/docs",
      buildId: "b1",
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
