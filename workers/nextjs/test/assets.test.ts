import { describe, expect, it } from "vitest";

import {
  contentTypeFor,
  serveStaticAsset,
  type AssetBucket,
  type AssetStoreDeps,
} from "../src/assets";

// A fake R2 bucket, keyed exactly as serveStaticAsset composes its key:
// "<prefix><pathname>".
function bucketServing(
  files: Record<string, { body: string; etag?: string; contentType?: string }>,
): AssetBucket {
  return {
    async get(key) {
      const file = files[key];
      if (!file) return null;
      return {
        body: new Blob([file.body]).stream(),
        httpEtag: file.etag,
        httpMetadata: file.contentType ? { contentType: file.contentType } : undefined,
      };
    },
  };
}

// Counts put()s against the real workerd cache so a test can assert write
// cardinality without reimplementing the Cache API. Each test uses a unique
// request URL, mirroring cache.test.ts's own isolation strategy under
// isolatedStorage: false.
function countingDeps(
  store: AssetBucket | undefined,
  prefix: string,
): AssetStoreDeps & { puts: number; flush: () => Promise<void> } {
  const real = caches.default;
  const pending: Promise<unknown>[] = [];
  const deps = {
    store,
    assetPrefix: prefix,
    puts: 0,
    cache: {
      match: (...args: Parameters<Cache["match"]>) => real.match(...args),
      put: (...args: Parameters<Cache["put"]>) => {
        deps.puts++;
        return real.put(...args);
      },
    },
    waitUntil: (promise: Promise<unknown>) => {
      pending.push(promise);
    },
    flush: async () => {
      await Promise.all(pending.splice(0));
    },
  };
  return deps;
}

describe("contentTypeFor", () => {
  it("infers content-type from the file extension", () => {
    expect(contentTypeFor("/next.svg")).toBe("image/svg+xml");
    expect(contentTypeFor("/_next/static/chunks/a.js")).toBe("text/javascript; charset=utf-8");
    expect(contentTypeFor("/styles.css")).toBe("text/css; charset=utf-8");
  });

  it("falls back to application/octet-stream for an unknown or missing extension", () => {
    expect(contentTypeFor("/README")).toBe("application/octet-stream");
    expect(contentTypeFor("/data.unknownext")).toBe("application/octet-stream");
  });
});

describe("serveStaticAsset", () => {
  it("reads the object at <prefix><pathname> and serves it with immutable headers", async () => {
    const url = new URL("https://serve-1.example/next.svg");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/next.svg": { body: "<svg/>", etag: "abc" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
    expect(res.headers.get("content-type")).toBe("image/svg+xml");
    expect(res.headers.get("cache-control")).toBe("public, max-age=31536000, immutable");
    expect(res.headers.get("etag")).toBe("abc");
  });

  it("serves the object's stored content-type when R2 carries one", async () => {
    const url = new URL("https://serve-ct-1.example/download");
    const deps = countingDeps(
      // A path with no extension the fallback could resolve: only the stored
      // metadata can produce the right type.
      bucketServing({ "assets/p/app/b1/download": { body: "hi", contentType: "application/pdf" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("application/pdf");
  });

  it("falls back to extension inference when the object carries no content-type", async () => {
    const url = new URL("https://serve-ct-2.example/styles.css");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/styles.css": { body: "body{}" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("text/css; charset=utf-8");
  });

  it("returns a plain 404 when the object is not in the store", async () => {
    const url = new URL("https://serve-2.example/missing.txt");
    const deps = countingDeps(bucketServing({}), "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
  });

  it("returns 404 when no store is bound", async () => {
    const url = new URL("https://serve-3.example/next.svg");
    const deps = countingDeps(undefined, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
  });

  it("serves a colo cache hit without reading the store again", async () => {
    const url = new URL("https://serve-4.example/next.svg");
    let reads = 0;
    const store: AssetBucket = {
      async get(key) {
        reads++;
        return key === "assets/p/app/b1/next.svg"
          ? { body: new Blob(["<svg/>"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const first = await serveStaticAsset(new Request(url), url, deps);
    expect(first.status).toBe(200);
    await deps.flush();
    expect(deps.puts).toBe(1);

    const second = await serveStaticAsset(new Request(url), url, deps);
    expect(second.status).toBe(200);
    expect(await second.text()).toBe("<svg/>");
    expect(reads).toBe(1);
  });
});
