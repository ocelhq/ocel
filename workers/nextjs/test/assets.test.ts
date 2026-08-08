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

  // The types Next.js itself serves for file-based metadata routes, which the
  // host mime database the deploy used to stamp from disagrees with.
  it("serves the file-based metadata routes as Next.js does", () => {
    expect(contentTypeFor("/favicon.ico")).toBe("image/x-icon");
    expect(contentTypeFor("/sitemap.xml")).toBe("application/xml");
    expect(contentTypeFor("/robots.txt")).toBe("text/plain");
    expect(contentTypeFor("/manifest.webmanifest")).toBe("application/manifest+json");
    expect(contentTypeFor("/icons/static/apple-icon.png")).toBe("image/png");
  });

  // Next keys this one off the file name, not the extension.
  it("serves app/manifest.json as a web manifest rather than as JSON", () => {
    expect(contentTypeFor("/manifest.json")).toBe("application/manifest+json");
    expect(contentTypeFor("/data.json")).toBe("application/json; charset=utf-8");
  });

  // A dot in a directory name is not an extension.
  it("reads the extension off the file name alone", () => {
    expect(contentTypeFor("/v1.0/README")).toBe("application/octet-stream");
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

  // Objects uploaded by a deploy that still stamped one carry a type the host
  // mime database chose, which is not the type Next.js serves.
  it("ignores any content-type the stored object carries", async () => {
    const url = new URL("https://serve-ct-1.example/favicon.ico");
    const deps = countingDeps(
      bucketServing({
        "assets/p/app/b1/favicon.ico": {
          body: "icon",
          contentType: "image/vnd.microsoft.icon",
        },
      }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/x-icon");
  });

  it("infers the content-type from the name the object is stored under", async () => {
    const url = new URL("https://serve-ct-2.example/styles.css");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/styles.css": { body: "body{}" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("text/css; charset=utf-8");
  });

  it("serves the app's own 404 page when the object is not in the store", async () => {
    const url = new URL("https://serve-2.example/blog/timm");
    const deps = countingDeps(
      bucketServing({
        "assets/p/app/b1/404.html": { body: "<h1>This page could not be found</h1>" },
      }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toContain("page could not be found");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
  });

  it("returns a plain 404 when the build emitted no 404 page", async () => {
    const url = new URL("https://serve-2b.example/missing.txt");
    const deps = countingDeps(bucketServing({}), "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
  });

  it("returns 404 when no store is bound", async () => {
    const url = new URL("https://serve-3.example/next.svg");
    const deps = countingDeps(undefined, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
  });

  it("resolves a route to the .html document the build stored it as", async () => {
    const url = new URL("https://serve-html-1.example/some");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/some.html": { body: "<html>some</html>" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>some</html>");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
  });

  // The dispatch map keys the error pages on /404 and /500; the documents are
  // stored as the files they are. Same resolution, no second rule.
  it("resolves a directly requested error page to its document", async () => {
    const url = new URL("https://serve-html-2.example/404");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/404.html": { body: "<h1>gone</h1>" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
  });

  it("still serves an extensionless file stored under its own name", async () => {
    const url = new URL("https://serve-html-3.example/LICENSE");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/LICENSE": { body: "MIT" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("MIT");
    expect(res.headers.get("content-type")).toBe("application/octet-stream");
  });

  it("reads only the requested key for a path that already names a file", async () => {
    const url = new URL("https://serve-html-4.example/some.rsc");
    const keys: string[] = [];
    const store: AssetBucket = {
      async get(key) {
        keys.push(key);
        return key === "assets/p/app/b1/some.rsc"
          ? { body: new Blob(["RSC"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("RSC");
    expect(keys).toEqual(["assets/p/app/b1/some.rsc"]);
  });

  // A route whose last segment carries what only looks like an extension is
  // still a page, and is still stored as the document it is.
  it("resolves a dotted route to its document once its own name misses", async () => {
    const url = new URL("https://serve-html-5.example/v1.0");
    const keys: string[] = [];
    const store: AssetBucket = {
      async get(key) {
        keys.push(key);
        return key === "assets/p/app/b1/v1.0.html"
          ? { body: new Blob(["<html>v1</html>"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>v1</html>");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(keys).toEqual([
      "assets/p/app/b1/v1.0",
      "assets/p/app/b1/v1.0.html",
    ]);
  });

  // The page a browser navigates to is the hit worth spending one read on.
  it("reads only the document for an extensionless page", async () => {
    const url = new URL("https://serve-html-6.example/some");
    const keys: string[] = [];
    const store: AssetBucket = {
      async get(key) {
        keys.push(key);
        return key === "assets/p/app/b1/some.html"
          ? { body: new Blob(["<html>some</html>"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(keys).toEqual(["assets/p/app/b1/some.html"]);
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
