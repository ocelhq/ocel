import { describe, expect, it } from "vitest";

import {
  serveStaticAsset,
  type AssetBucket,
  type AssetStoreDeps,
} from "@framework/next-router/assets";

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

describe("serveStaticAsset", () => {
  it("reads the object at <prefix><pathname> and serves it", async () => {
    const url = new URL("https://serve-1.example/next.svg");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/next.svg": { body: "<svg/>", etag: "abc" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
    expect(res.headers.get("content-type")).toBe("image/svg+xml");
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
    expect(res.headers.get("etag")).toBe("abc");
  });

  it("serves a content-hashed chunk with immutable headers", async () => {
    const url = new URL("https://serve-1b.example/_next/static/chunks/main.js");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/_next/static/chunks/main.js": { body: "1" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("cache-control")).toBe("public, max-age=31536000, immutable");
  });

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

  it("serves the locale's own 404 page when the request resolved to one", async () => {
    const url = new URL("https://serve-2i.example/blog/timm");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/fr/404.html": { body: "<h1>introuvable</h1>" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps, "fr");

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<h1>introuvable</h1>");
  });

  it("falls back to the bare 404 page when the locale emitted none", async () => {
    const url = new URL("https://serve-2j.example/blog/timm");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/404.html": { body: "<h1>gone</h1>" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps, "fr");

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<h1>gone</h1>");
  });

  it("returns a plain 404 when the build emitted no 404 page", async () => {
    const url = new URL("https://serve-2b.example/missing.txt");
    const deps = countingDeps(bucketServing({}), "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
  });

  it("answers a _next/static miss in plain text, not with the 404 page", async () => {
    const url = new URL("https://serve-2c.example/_next/static/chunks/gone.js");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/404.html": { body: "<h1>gone</h1>" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("Not Found");
    expect(res.headers.get("content-type")).toBe("text/plain; charset=utf-8");
  });

  it("answers a _next/static miss in plain text under a basePath", async () => {
    const url = new URL("https://serve-2d.example/docs/_next/static/chunks/gone.js");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/404.html": { body: "<h1>gone</h1>" } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toBe("text/plain; charset=utf-8");
  });

  it("answers a _next/static miss in plain text when no store is bound", async () => {
    const url = new URL("https://serve-2e.example/_next/static/chunks/gone.js");
    const deps = countingDeps(undefined, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toBe("text/plain; charset=utf-8");
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

  it("resolves the root request to the index.html document", async () => {
    const url = new URL("https://serve-root-1.example/");
    const keys: string[] = [];
    const store: AssetBucket = {
      async get(key) {
        keys.push(key);
        return key === "assets/p/app/b1/index.html"
          ? { body: new Blob(["<html>root</html>"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>root</html>");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(keys).toEqual(["assets/p/app/b1/index.html"]);
  });

  it("falls back to the bare root name when index.html is missing", async () => {
    const url = new URL("https://serve-root-1b.example/");
    const keys: string[] = [];
    const store: AssetBucket = {
      async get(key) {
        keys.push(key);
        return key === "assets/p/app/b1/"
          ? { body: new Blob(["<html>legacy root</html>"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>legacy root</html>");
    expect(keys).toEqual(["assets/p/app/b1/index.html", "assets/p/app/b1/"]);
  });

  it("resolves a basePath root request to <basePath>/index.html", async () => {
    const url = new URL("https://serve-root-2.example/docs");
    const deps = countingDeps(
      bucketServing({
        "assets/p/app/b1/docs/index.html": { body: "<html>docs root</html>" },
      }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(new Request(url), url, {
      ...deps,
      basePath: "/docs",
    });

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>docs root</html>");
  });

  it("does not treat an ordinary page as a basePath root when no basePath is configured", async () => {
    const url = new URL("https://serve-root-3.example/docs");
    const keys: string[] = [];
    const store: AssetBucket = {
      async get(key) {
        keys.push(key);
        return key === "assets/p/app/b1/docs.html"
          ? { body: new Blob(["<html>docs page</html>"]).stream() }
          : null;
      },
    };
    const deps = countingDeps(store, "assets/p/app/b1");

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<html>docs page</html>");
    expect(keys).toEqual(["assets/p/app/b1/docs.html"]);
  });

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

  it("answers 304 when the client already holds the object's etag", async () => {
    const url = new URL("https://serve-304-1.example/_next/static/service-worker/sw.js");
    const deps = countingDeps(
      bucketServing({
        "assets/p/app/b1/_next/static/service-worker/sw.js": { body: "sw", etag: '"v1"' },
      }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(
      new Request(url, { headers: { "if-none-match": '"v1"' } }),
      url,
      deps,
    );

    expect(res.status).toBe(304);
    expect(res.body).toBe(null);
    expect(res.headers.get("etag")).toBe('"v1"');
    expect(res.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });

  it("answers 304 for a weak or listed etag, and for *", async () => {
    const files = { "assets/p/app/b1/robots.txt": { body: "ok", etag: '"v1"' } };
    const cases = ['W/"v1"', '"v0", "v1"', "*"];

    for (const [i, header] of cases.entries()) {
      const url = new URL(`https://serve-304-2-${i}.example/robots.txt`);
      const res = await serveStaticAsset(
        new Request(url, { headers: { "if-none-match": header } }),
        url,
        countingDeps(bucketServing(files), "assets/p/app/b1"),
      );
      expect(res.status, header).toBe(304);
    }
  });

  it("serves the body when the client holds a different etag", async () => {
    const url = new URL("https://serve-304-3.example/robots.txt");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/robots.txt": { body: "ok", etag: '"v2"' } }),
      "assets/p/app/b1",
    );

    const res = await serveStaticAsset(
      new Request(url, { headers: { "if-none-match": '"v1"' } }),
      url,
      deps,
    );

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("ok");
  });

  it("writes only the immutable assets to the colo cache", async () => {
    const url = new URL("https://serve-put-1.example/favicon.ico");
    const deps = countingDeps(
      bucketServing({ "assets/p/app/b1/favicon.ico": { body: "icon" } }),
      "assets/p/app/b1",
    );

    expect((await serveStaticAsset(new Request(url), url, deps)).status).toBe(200);
    await deps.flush();

    expect(deps.puts).toBe(0);
  });

  it("serves a colo cache hit without reading the store again", async () => {
    const url = new URL("https://serve-4.example/_next/static/chunks/main.js");
    let reads = 0;
    const store: AssetBucket = {
      async get(key) {
        reads++;
        return key === "assets/p/app/b1/_next/static/chunks/main.js"
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
