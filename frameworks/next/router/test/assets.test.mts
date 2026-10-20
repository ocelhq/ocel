import { objectKey, storagePrefix } from "@platform/edge-contract/storage-key";
import { describe, expect, it } from "vitest";
import { type AssetStoreDeps, contentTypeFor, serveStaticAsset } from "../src/assets.mjs";

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

  it("serves the file-based metadata routes as Next.js does", () => {
    expect(contentTypeFor("/favicon.ico")).toBe("image/x-icon");
    expect(contentTypeFor("/sitemap.xml")).toBe("application/xml");
    expect(contentTypeFor("/products/sitemap.xml")).toBe("application/xml");
    expect(contentTypeFor("/robots.txt")).toBe("text/plain");
    expect(contentTypeFor("/manifest.webmanifest")).toBe("application/manifest+json");
    expect(contentTypeFor("/icons/static/apple-icon.png")).toBe("image/png");
  });

  it("types the metadata routes Next keys off a file name by that name", () => {
    expect(contentTypeFor("/manifest.json")).toBe("application/manifest+json");
    expect(contentTypeFor("/data.json")).toBe("application/json; charset=utf-8");
    expect(contentTypeFor("/robots.txt")).toBe("text/plain");
    expect(contentTypeFor("/notes.txt")).toBe("text/plain; charset=utf-8");
  });

  it("answers a file named after an object prototype member as unknown", () => {
    expect(contentTypeFor("/constructor")).toBe("application/octet-stream");
    expect(contentTypeFor("/__proto__")).toBe("application/octet-stream");
    expect(contentTypeFor("/x.toString")).toBe("application/octet-stream");
  });

  it("reads the extension off the file name alone", () => {
    expect(contentTypeFor("/v1.0/README")).toBe("application/octet-stream");
  });
});

function basePathStoreServing(files: Record<string, string>): AssetStoreDeps {
  return {
    store: {
      async get(key) {
        const body = files[key];
        return body === undefined ? null : { body: new Blob([body]).stream() };
      },
    },
    storage: { project: "p", env: "prod", app: "app", release: "r00000b01" },
    basePath: "/docs",
    static: { immutablePrefixes: ["/docs/_next/static/"] },
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

describe("serveStaticAsset under a basePath", () => {
  it("serves the 404 page the build stored under the basePath when a page misses", async () => {
    const url = new URL("https://app.example/docs/missing");
    const deps = basePathStoreServing({
      "assets/p/prod/app/r00000b01/docs/404.html": "<h1>gone</h1>",
    });

    const res = await serveStaticAsset(new Request(url), url, deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<h1>gone</h1>");
    expect(res.headers.get("content-type")).toBe("text/html; charset=utf-8");
  });

  it("serves the locale's 404 page the build stored under the basePath", async () => {
    const url = new URL("https://app.example/docs/fr/missing");
    const deps = basePathStoreServing({
      "assets/p/prod/app/r00000b01/docs/fr/404.html": "<h1>introuvable</h1>",
      "assets/p/prod/app/r00000b01/docs/404.html": "<h1>gone</h1>",
    });

    const res = await serveStaticAsset(new Request(url), url, deps, "fr");

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<h1>introuvable</h1>");
  });
});

describe("serveStaticAsset beside a release's route table", () => {
  const storage = { project: "shop", env: "prod", app: "web", release: "r0a1b2c3d" };
  const table = `${"a".repeat(64)}.json`;

  it("reads no key outside the asset prefix, however the path climbs out of it", async () => {
    const asked: string[] = [];
    const deps: AssetStoreDeps = {
      store: {
        async get(key) {
          asked.push(key);
          return key === objectKey("route-tables", storage, table)
            ? { body: new Blob(['{"bypassToken":"secret"}']).stream() }
            : null;
        },
      },
      storage,
      cache: { match: async () => undefined, put: async () => {} },
      waitUntil: () => {},
    };

    for (const path of [
      `/../route-table/${table}`,
      `/%2e%2e/route-table/${table}`,
      `/..%2froute-table/${table}`,
    ]) {
      const url = new URL(`https://app.example${path}`);
      const res = await serveStaticAsset(new Request(url), url, deps);

      expect(res.status, path).toBe(404);
      expect(await res.text(), path).not.toContain("secret");
    }
    expect(asked.every((key) => key.startsWith(storagePrefix("assets", storage)))).toBe(true);
  });
});
