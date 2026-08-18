import { describe, expect, it } from "vitest";

import { cacheControlFor, contentTypeFor } from "../src/assets.mjs";

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

describe("cacheControlFor", () => {
  const immutable = "public, max-age=31536000, immutable";
  const revalidate = "public, max-age=0, must-revalidate";

  it("makes the content-hashed _next/static chunks immutable", () => {
    expect(cacheControlFor("/_next/static/chunks/main-abc123.js")).toBe(immutable);
    expect(cacheControlFor("/_next/static/css/app.css")).toBe(immutable);
    expect(cacheControlFor("/_next/static/media/font.woff2")).toBe(immutable);
  });

  it("exempts the service-worker chunk", () => {
    expect(cacheControlFor("/_next/static/service-worker/sw.js")).toBe(revalidate);
  });

  it("classifies a basePath app's assets the same way", () => {
    expect(cacheControlFor("/docs/_next/static/chunks/main.js")).toBe(immutable);
    expect(cacheControlFor("/docs/_next/static/service-worker/sw.js")).toBe(revalidate);
  });

  it("revalidates every asset served at a stable URL", () => {
    expect(cacheControlFor("/favicon.ico")).toBe(revalidate);
    expect(cacheControlFor("/sitemap.xml")).toBe(revalidate);
    expect(cacheControlFor("/icons/static/apple-icon.png")).toBe(revalidate);
    expect(cacheControlFor("/next.svg")).toBe(revalidate);
    expect(cacheControlFor("/some.html")).toBe(revalidate);
  });
});
