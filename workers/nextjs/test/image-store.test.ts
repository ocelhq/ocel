// The durable tier under the colo cache: colo -> R2 -> optimizer. The colo cache
// is the real workerd one and is process-wide, so every test names its own slug
// (the key is a hash over it) and evicts its own colo entry by hand wherever the
// point is what the tier *below* colo answers. The store is a fake — a real
// optimizer would 502 here, since no artifact digest is pinned yet.
import { describe, expect, it } from "vitest";

import type { CacheDeps } from "../src/cache";
import {
  serveImage,
  unprovisionedImageOrigin,
  type ImageConfig,
  type ImageDeps,
} from "../src/image";
import type { ImagePutOptions, ImageStore } from "../src/image-store";
import fixtures from "./fixtures/image-conformance.json";
import { coloDeps } from "./cache-deps";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]
  .config;

interface FakeStore extends ImageStore {
  objects: Map<string, { body: Uint8Array; customMetadata?: Record<string, string> }>;
  gets: string[];
  puts: string[];
  failGet: boolean;
  failPut: boolean;
  // Held open by the concurrency test so a second request is guaranteed to
  // arrive while the first write is still in flight.
  blockPut?: Promise<void>;
}

function fakeStore(): FakeStore {
  const store: FakeStore = {
    objects: new Map(),
    gets: [],
    puts: [],
    failGet: false,
    failPut: false,
    get: async (key) => {
      store.gets.push(key);
      if (store.failGet) throw new Error("the store is unreachable");
      const object = store.objects.get(key);
      if (!object) return null;
      return {
        body: new Blob([object.body as BlobPart]).stream(),
        customMetadata: object.customMetadata,
      };
    },
    put: async (key: string, value: ArrayBuffer, options?: ImagePutOptions) => {
      store.puts.push(key);
      if (store.blockPut) await store.blockPut;
      if (store.failPut) throw new Error("the store is unreachable");
      store.objects.set(key, {
        body: new Uint8Array(value),
        customMetadata: options?.customMetadata,
      });
    },
  };
  return store;
}

// Delegates to the real colo cache while recording the keys written, so a test
// can reach — and evict — the entry the worker addressed without reimplementing
// the key.
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

const OPTIMIZED = "optimized-bytes";

const OPTIMIZER_HEADERS = {
  "cache-control": "public, max-age=60",
  "content-type": "image/webp",
  etag: '"abc123"',
  "content-disposition": 'inline; filename="a.webp"',
  "content-security-policy": "script-src 'none'; sandbox;",
  "x-content-type-options": "nosniff",
};

function optimizer(body = OPTIMIZED, init: ResponseInit = {}) {
  const fn = Object.assign(
    async () => {
      fn.calls++;
      return new Response(body, {
        status: 200,
        headers: OPTIMIZER_HEADERS,
        ...init,
      });
    },
    { calls: 0 },
  );
  return fn;
}

interface Harness {
  deps: ImageDeps;
  store: FakeStore;
  origin: { calls: number };
  clock: { ms: number };
  flush(): Promise<void>;
  evictColo(): Promise<void>;
  coloKey(): string;
  objectKey(): string;
}

function harness(
  slug: string,
  overrides: Partial<ImageDeps> & { origin?: ImageDeps["origin"] } = {},
): Harness {
  const clock = { ms: 0 };
  const pending: Promise<unknown>[] = [];
  const recording = recordingCache();
  const cache: CacheDeps = coloDeps({
    cache: recording,
    now: () => clock.ms,
    waitUntil: (promise) => {
      pending.push(promise);
    },
  });
  const store = fakeStore();
  const origin = overrides.origin ?? optimizer();

  return {
    clock,
    store,
    origin: origin as { calls: number },
    deps: {
      config: BASE_CONFIG,
      basePath: "",
      slug,
      app: "web",
      buildId: "b1",
      origin: unprovisionedImageOrigin,
      cache,
      imageStore: store,
      ...overrides,
      ...(overrides.origin ? {} : { origin }),
    },
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
    evictColo: async () => {
      for (const key of recording.keys.splice(0)) {
        await caches.default.delete(new Request(key));
      }
    },
    coloKey: () => recording.keys[0],
    objectKey: () => `images/${slug}/${new URL(recording.keys[0]).pathname.slice(1)}`,
  };
}

const IMAGE = "url=%2Fa.png&w=640&q=75";
// legacy.example is in the fixture config's `domains`, so this is a remote
// source whose identity is the url and not a build-time content hash.
const REMOTE = `url=${encodeURIComponent("https://legacy.example/a.png")}&w=640&q=75`;
// minimumCacheTTL from the fixture config wins over the optimizer's own max-age,
// so it is both the browser's window and the colo entry's.
const SERVED_TTL = "public, max-age=14400, must-revalidate";
// Past that window, so the colo entry is stale and its refresh runs.
const AFTER_WINDOW = 14_400_001;

function get(deps: ImageDeps, search = IMAGE, init?: RequestInit) {
  const url = new URL(`https://app.example/_next/image?${search}`);
  return serveImage(new Request(url, init), url, deps);
}

describe("the durable image tier", () => {
  // The whole read order in one test: the first request misses both tiers and
  // writes both, the second (with colo evicted, as a second PoP would see it)
  // is answered by the store without the optimizer, and the third is answered
  // by the colo entry that serve mirrored back.
  it("serves an R2 hit as PRERENDER and mirrors it into colo", async () => {
    const h = harness("r2-hit");

    const miss = await get(h.deps);
    expect(miss.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await miss.text()).toBe(OPTIMIZED);
    await h.flush();

    const objectKey = h.objectKey();
    await h.evictColo();
    h.store.gets.length = 0;

    const fromStore = await get(h.deps);
    expect(fromStore.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(fromStore.headers.get("x-nextjs-cache")).toBe("HIT");
    expect(fromStore.headers.get("content-type")).toBe("image/webp");
    expect(await fromStore.text()).toBe(OPTIMIZED);
    expect(h.store.gets).toEqual([objectKey]);
    expect(h.origin.calls).toBe(1);
    await h.flush();

    // Mirrored: the colo tier answers the next request without going down.
    h.store.gets.length = 0;
    h.clock.ms = 1_000;
    const hit = await get(h.deps);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await hit.text()).toBe(OPTIMIZED);
    expect(h.store.gets).toEqual([]);
    expect(h.origin.calls).toBe(1);
  });

  // An image entry is never expired, only ever stale, so its background refresh
  // is the only path back to the optimizer there is. A refresh that read this
  // tier would answer itself with the bytes that went stale and rewrite the colo
  // entry fresh from them — and the optimizer would be unreachable for that key
  // for as long as the object lived.
  it("re-optimizes a stale remote image instead of answering the refresh from the store", async () => {
    const h = harness("r2-stale-remote");

    await (await get(h.deps, REMOTE)).text();
    await h.flush();
    expect(h.origin.calls).toBe(1);
    const objectKey = h.objectKey();
    h.store.gets.length = 0;
    h.store.puts.length = 0;

    h.clock.ms = AFTER_WINDOW;
    const stale = await get(h.deps, REMOTE);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await stale.text()).toBe(OPTIMIZED);
    await h.flush();

    // The refresh went to the optimizer, and never asked the store first.
    expect(h.origin.calls).toBe(2);
    expect(h.store.gets).toEqual([]);
    expect(h.store.puts).toEqual([objectKey]);

    // Both tiers rewritten: the next request is a colo hit, with no third
    // optimization behind it.
    const hit = await get(h.deps, REMOTE);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await hit.text()).toBe(OPTIMIZED);
    expect(h.origin.calls).toBe(2);
  });

  // The other half of that rule. A local source is named by the build's content
  // hash of the file, so the stored object is the only thing its key can ever
  // name and re-optimizing could only reproduce it — one wasted optimization per
  // key per window.
  it("refreshes a stale local image from the store rather than the optimizer", async () => {
    const h = harness("r2-stale-local");

    await (await get(h.deps)).text();
    await h.flush();
    expect(h.origin.calls).toBe(1);
    const objectKey = h.objectKey();
    h.store.gets.length = 0;

    h.clock.ms = AFTER_WINDOW;
    const stale = await get(h.deps);
    expect(stale.headers.get("x-ocel-cache")).toBe("STALE");
    expect(await stale.text()).toBe(OPTIMIZED);
    await h.flush();

    expect(h.origin.calls).toBe(1);
    expect(h.store.gets).toEqual([objectKey]);

    const hit = await get(h.deps);
    expect(hit.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await hit.text()).toBe(OPTIMIZED);
    expect(h.origin.calls).toBe(1);
  });

  // A static-import url and its public/ twin are one file, hash to one key and
  // share one object. Only the hashed url may call the bytes immutable, so the
  // claim has to be applied per request and must never have been stored: an
  // entry warmed through the hashed url and served through the twin would
  // otherwise pin a ten-year unrevalidatable answer on a mutable path.
  it("serves a public/ twin from the static import's object without immutable", async () => {
    const hash = "beef".repeat(16);
    const h = harness("r2-twin", {
      assetHashes: {
        "/_next/static/media/logo.abc123.png": hash,
        "/logo.png": hash,
      },
    });

    const warm = await get(
      h.deps,
      "url=%2F_next%2Fstatic%2Fmedia%2Flogo.abc123.png&w=640&q=75",
    );
    expect(warm.headers.get("cache-control")).toBe(
      "public, max-age=315360000, immutable",
    );
    await warm.text();
    await h.flush();
    const objectKey = h.objectKey();
    await h.evictColo();
    h.store.gets.length = 0;

    const twin = await get(h.deps, "url=%2Flogo.png&w=640&q=75");
    expect(twin.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(twin.headers.get("cache-control")).toBe(SERVED_TTL);
    expect(await twin.text()).toBe(OPTIMIZED);
    expect(h.store.gets).toEqual([objectKey]);
    expect(h.store.objects.size).toBe(1);
    expect(h.origin.calls).toBe(1);
  });

  // The object key is a hash over inputs anyone can compute, and the R2 token
  // behind the bucket is bucket-scoped, so a planted entry is a reachable shape:
  // the stored header list is input, not this worker's own word.
  it("reconstructs a planted entry from the allowlist and always sends nosniff", async () => {
    const h = harness("r2-planted");

    await (await get(h.deps)).text();
    await h.flush();
    const objectKey = h.objectKey();
    await h.evictColo();

    h.store.objects.set(objectKey, {
      body: new TextEncoder().encode("<script>alert(1)</script>"),
      customMetadata: {
        "ocel-image-version": "1",
        "ocel-image-headers": JSON.stringify({
          "content-type": "text/html",
          "x-content-type-options": "",
          "set-cookie": "session=stolen",
          "access-control-allow-origin": "*",
          "content-security-policy": "script-src 'unsafe-inline'",
        }),
      },
    });

    const served = await get(h.deps);
    expect(served.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(served.headers.get("x-content-type-options")).toBe("nosniff");
    expect(served.headers.get("set-cookie")).toBeNull();
    expect(served.headers.get("access-control-allow-origin")).toBeNull();
    // Allowlisted, so these do come back — which is what makes the two above
    // the allowlist's doing and not an empty entry's.
    expect(served.headers.get("content-type")).toBe("text/html");
    expect(served.headers.get("content-security-policy")).toBe(
      "script-src 'unsafe-inline'",
    );
  });

  // R2 rejects the whole put over its metadata cap, and a rejected put is
  // indistinguishable from an outage — so the tier would disable itself for
  // every image in the app with nothing said.
  it("skips the write when the entry's metadata exceeds the budget", async () => {
    const csp = `script-src 'none'; sandbox; report-uri /${"x".repeat(4000)};`;
    const h = harness("r2-oversized", {
      origin: optimizer(OPTIMIZED, {
        headers: { ...OPTIMIZER_HEADERS, "content-security-policy": csp },
      }),
    });

    const response = await get(h.deps);
    expect(response.status).toBe(200);
    expect(response.headers.get("content-security-policy")).toBe(csp);
    expect(response.headers.get("cache-control")).toBe(SERVED_TTL);
    expect(await response.text()).toBe(OPTIMIZED);

    await h.flush();
    expect(h.store.gets).toHaveLength(1);
    expect(h.store.puts).toEqual([]);
    expect(h.store.objects.size).toBe(0);
  });

  // The tier may only engage where the colo tier above it does. A POST is served
  // uncached and stamped BYPASS; a store that answered it anyway would be
  // serving stored bytes under a header that says it did not.
  it("never touches the store on a method the cache may not answer", async () => {
    const h = harness("r2-post");

    const response = await get(h.deps, IMAGE, { method: "POST" });
    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(response.headers.get("x-nextjs-cache")).toBe("MISS");
    expect(await response.text()).toBe(OPTIMIZED);
    await h.flush();
    expect(h.store.gets).toEqual([]);
    expect(h.store.puts).toEqual([]);

    // The same request as a GET does reach the store, so the two above are the
    // method gate and not an inert test.
    await (await get(h.deps)).text();
    await h.flush();
    expect(h.store.gets).toHaveLength(1);
    expect(h.store.puts).toHaveLength(1);
  });

  // The same rule the colo tier stores by: a max-age of zero is the whole of
  // what "do not keep this" can look like on an image response.
  it("never writes a 200 the colo tier would not keep either", async () => {
    const h = harness("r2-unstorable", {
      config: { ...BASE_CONFIG, minimumCacheTTL: 0 },
      origin: optimizer(OPTIMIZED, {
        headers: { ...OPTIMIZER_HEADERS, "cache-control": "public, max-age=0" },
      }),
    });

    const response = await get(h.deps);
    expect(response.status).toBe(200);
    expect(response.headers.get("cache-control")).toBe(
      "public, max-age=0, must-revalidate",
    );
    await response.text();
    await h.flush();

    expect(h.store.gets).toHaveLength(1);
    expect(h.store.puts).toEqual([]);
    expect(h.store.objects.size).toBe(0);
  });

  // The colo tier dedups its own put by the colo key; this tier's object key is
  // a different string, so it needs its own entry in the same in-flight set.
  it("issues one put for a burst of concurrent misses on a key", async () => {
    const h = harness("r2-burst");
    let release = () => {};
    h.store.blockPut = new Promise<void>((resolve) => {
      release = resolve;
    });

    const responses = await Promise.all([get(h.deps), get(h.deps)]);
    await Promise.all(responses.map((response) => response.text()));

    expect(h.store.puts).toEqual([h.objectKey()]);
    release();
    await h.flush();
    expect(h.store.objects.size).toBe(1);
  });

  it("writes both tiers on a miss, at images/<slug>/<cacheKey>", async () => {
    const h = harness("r2-write");

    const miss = await get(h.deps);
    expect(miss.headers.get("x-ocel-cache")).toBe("MISS");
    await miss.text();
    await h.flush();

    const digest = new URL(h.coloKey()).pathname.slice(1);
    expect(digest).toMatch(/^[0-9a-f]{64}$/);
    expect(h.store.puts).toEqual([`images/r2-write/${digest}`]);
    expect(h.origin.calls).toBe(1);

    // The colo write landed too — the entry is readable at the key it recorded.
    const stored = await caches.default.match(new Request(h.coloKey()));
    expect(stored).toBeDefined();
    expect(stored?.headers.get("x-ocel-entry-version")).toBe("1");
  });

  // A write that fails costs a future cache miss and nothing else. It must not
  // reach the response, and the waitUntil promise must not reject.
  it("serves normally when the store refuses the write", async () => {
    const h = harness("r2-put-fails");
    h.store.failPut = true;

    const response = await get(h.deps);
    expect(response.status).toBe(200);
    expect(response.headers.get("x-ocel-cache")).toBe("MISS");
    expect(response.headers.get("cache-control")).toBe(
      "public, max-age=14400, must-revalidate",
    );
    expect(await response.text()).toBe(OPTIMIZED);

    await expect(h.flush()).resolves.toBeUndefined();
    // The write was attempted and refused — not skipped, which would pass this
    // test for the wrong reason.
    expect(h.store.puts).toEqual([h.objectKey()]);
    expect(h.store.objects.size).toBe(0);
  });

  it("falls through to the optimizer when the store refuses the read", async () => {
    const h = harness("r2-get-fails");
    h.store.failGet = true;

    const response = await get(h.deps);
    expect(response.status).toBe(200);
    expect(response.headers.get("x-ocel-cache")).toBe("MISS");
    expect(await response.text()).toBe(OPTIMIZED);
    expect(h.store.gets).toHaveLength(1);
    expect(h.origin.calls).toBe(1);
  });

  // The criterion the images/ prefix exists for: the object sits outside any
  // buildId, so a redeploy that did not touch the bytes still answers from it
  // even where the colo entry is gone.
  it("answers from the store under a later build with unchanged bytes", async () => {
    const assetHashes = { "/a.png": "c0ffee".repeat(10) };
    const h = harness("r2-redeploy", { assetHashes, buildId: "b1" });

    await (await get(h.deps)).text();
    await h.flush();
    const objectKey = h.objectKey();
    await h.evictColo();
    h.store.gets.length = 0;

    const redeployed = await get({ ...h.deps, buildId: "b2" });
    expect(redeployed.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(await redeployed.text()).toBe(OPTIMIZED);
    expect(h.store.gets).toEqual([objectKey]);
    expect(h.origin.calls).toBe(1);
  });

  // The live state of every substrate that bootstrapped no optimizer. Keeping it
  // would make one unprovisioned moment the answer for as long as the object
  // lives.
  it("never writes a non-200", async () => {
    const origin = Object.assign(
      async () => {
        origin.calls++;
        return new Response("no optimizer", { status: 502 });
      },
      { calls: 0 },
    );
    const h = harness("r2-502", { origin });

    const response = await get(h.deps);
    expect(response.status).toBe(502);
    await response.text();
    await h.flush();

    // The tier read the store and then declined to write it — the gate, not an
    // absent tier.
    expect(h.store.gets).toHaveLength(1);
    expect(h.store.puts).toEqual([]);
    expect(h.store.objects.size).toBe(0);
  });

  // Never a crash and never a serve of an entry that cannot be fully trusted:
  // the version is read before anything else is parsed, and each of these is a
  // miss the optimizer answers.
  it("treats an entry it cannot fully trust as a miss", async () => {
    for (const [name, mutate] of [
      ["missing", () => undefined],
      ["unparseable", () => ({ "ocel-image-version": "1", "ocel-image-headers": "{" })],
      [
        "wrong version",
        () => ({ "ocel-image-version": "99", "ocel-image-headers": "[]" }),
      ],
    ] as Array<[string, () => Record<string, string> | undefined]>) {
      const h = harness(`r2-untrusted-${name.replace(" ", "-")}`);

      await (await get(h.deps)).text();
      await h.flush();
      const objectKey = h.objectKey();
      await h.evictColo();

      const object = h.store.objects.get(objectKey)!;
      h.store.objects.set(objectKey, { ...object, customMetadata: mutate() });
      h.store.gets.length = 0;

      const response = await get(h.deps);
      expect([name, response.status]).toEqual([name, 200]);
      expect([name, response.headers.get("x-ocel-cache")]).toEqual([name, "MISS"]);
      expect(await response.text()).toBe(OPTIMIZED);
      expect([name, h.store.gets]).toEqual([name, [objectKey]]);
      expect([name, h.origin.calls]).toEqual([name, 2]);
    }
  });

  // The metadata is the entry's single source of truth, so everything the served
  // image carries has to come back off it — not just the two fields R2's own
  // httpMetadata has a slot for — and nothing else may. The exact header set,
  // not a subset of it: what must not have survived (the optimizer's passthrough
  // marker, the colo tier's x-ocel-entry-* bookkeeping, any framing header of a
  // body this tier decoded on the way in) is the half a subset check cannot see.
  it("round-trips exactly the headers the served image carries", async () => {
    const h = harness("r2-headers", {
      // The optimizer's own passthrough marker, and the bookkeeping the colo
      // tier writes beside an entry: both are the deployment's business, and
      // neither may reach the store or a browser.
      origin: optimizer(OPTIMIZED, {
        headers: {
          ...OPTIMIZER_HEADERS,
          "x-ocel-image-passthrough": "1",
          "x-ocel-entry-window": "s-maxage=99",
        },
      }),
    });

    await (await get(h.deps)).text();
    await h.flush();
    await h.evictColo();

    const fromStore = await get(h.deps);
    expect(Object.fromEntries(fromStore.headers)).toEqual({
      "x-ocel-cache": "PRERENDER",
      "content-type": "image/webp",
      "cache-control": SERVED_TTL,
      vary: "accept",
      etag: '"abc123"',
      "content-disposition": 'inline; filename="a.webp"',
      "content-security-policy": "script-src 'none'; sandbox;",
      "x-content-type-options": "nosniff",
      "x-nextjs-cache": "HIT",
    });
  });

  it("answers a HEAD from the store with the headers and no body", async () => {
    const h = harness("r2-head");

    await (await get(h.deps)).text();
    await h.flush();
    const objectKey = h.objectKey();
    await h.evictColo();
    h.store.gets.length = 0;

    const head = await get(h.deps, IMAGE, { method: "HEAD" });
    expect(head.headers.get("x-ocel-cache")).toBe("PRERENDER");
    expect(head.headers.get("x-nextjs-cache")).toBe("HIT");
    expect(head.headers.get("content-type")).toBe("image/webp");
    expect(head.headers.get("cache-control")).toBe(SERVED_TTL);
    expect(await head.text()).toBe("");
    // The store answered it, and the entry it mirrored back is a complete one:
    // the GET that follows is a colo hit carrying the body the HEAD dropped.
    expect(h.store.gets).toEqual([objectKey]);
    expect(h.origin.calls).toBe(1);
    await h.flush();

    const next = await get(h.deps);
    expect(next.headers.get("x-ocel-cache")).toBe("HIT");
    expect(await next.text()).toBe(OPTIMIZED);
    expect(h.origin.calls).toBe(1);
  });

  // The tier needs the waitUntil the cache deps carry, so it engages only
  // alongside them: with no colo cache bound the store is never consulted.
  it("stays out of the way when no colo cache is bound", async () => {
    const h = harness("r2-uncached");
    const response = await get({ ...h.deps, cache: undefined });

    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(await response.text()).toBe(OPTIMIZED);
    expect(h.store.gets).toEqual([]);
    expect(h.store.puts).toEqual([]);

    // The same request with the cache bound does reach it, so the store staying
    // untouched above is the guard and not an inert test.
    await (await get(h.deps)).text();
    await h.flush();
    expect(h.store.gets).toHaveLength(1);
    expect(h.store.puts).toHaveLength(1);
  });
});
