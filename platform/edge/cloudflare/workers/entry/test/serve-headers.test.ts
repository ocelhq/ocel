import { describe, expect, it } from "vitest";
import { forCache } from "../src/serve-headers";

const release = "r1a2b3c4d";
const pathname = "/blog/a";

function shaped(init: ResponseInit & { body?: string | null }): Response {
  const { body = "page", ...rest } = init;
  return forCache(new Response(body, rest), { release, pathname });
}

const stored = { "cache-control": "s-maxage=60" };

describe("the headers a cached response leaves Serve with", () => {
  it("makes a header-less 200 private and not stored", () => {
    const response = shaped({ status: 200 });
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
  });

  it.each([
    ["Set-Cookie", { "set-cookie": "a=1", "cache-control": "s-maxage=60" }],
    ["private", { "cache-control": "private, max-age=60" }],
    ["no-store", { "cache-control": "no-store, s-maxage=60" }],
    ["no-cache", { "cache-control": "no-cache, s-maxage=60" }],
  ])("leaves a response with %s uncacheable", (_name, headers) => {
    const response = shaped({ status: 200, headers: { ...headers, "cache-tag": "a" } });
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
    expect(response.headers.get("cache-tag")).toBeNull();
  });

  it("keeps the Set-Cookie of an uncacheable response", () => {
    const response = shaped({
      status: 200,
      headers: { "set-cookie": "a=1", "cache-control": "s-maxage=60" },
    });
    expect(response.headers.get("set-cookie")).toBe("a=1");
  });

  it("drops an origin's own edge directive from an uncacheable response", () => {
    const response = shaped({
      status: 200,
      headers: {
        "cache-control": "private",
        "cloudflare-cdn-cache-control": "max-age=600",
        "cdn-cache-control": "max-age=600",
      },
    });
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
    expect(response.headers.get("cdn-cache-control")).toBeNull();
  });

  it.each([201, 202, 206, 302, 307, 400, 403, 500, 503])("never stores a %i", (status) => {
    const response = shaped({ status, body: null, headers: stored });
    expect(response.status).toBe(status);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
  });

  it.each([200, 301, 308, 404, 410])("stores a %i that asks to be stored", (status) => {
    const redirect = status === 301 || status === 308;
    const response = shaped({
      status,
      body: status === 200 ? "page" : null,
      headers: redirect ? { ...stored, location: "/x" } : stored,
    });
    expect(response.status).toBe(status);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=60, stale-if-error=86400",
    );
  });

  it("rewrites s-maxage and stale-while-revalidate into the edge's own header", () => {
    const response = shaped({
      status: 200,
      headers: { "cache-control": "s-maxage=15, stale-while-revalidate=31535985" },
    });
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=15, stale-while-revalidate=31535985, stale-if-error=86400",
    );
    expect(response.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });

  it("takes max-age as the edge's lifetime when there is no s-maxage", () => {
    const response = shaped({ status: 200, headers: { "cache-control": "public, max-age=120" } });
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=120, stale-if-error=86400",
    );
    expect(response.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });

  it("prefers s-maxage to max-age", () => {
    const response = shaped({
      status: 200,
      headers: { "cache-control": "max-age=5, s-maxage=90" },
    });
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=90, stale-if-error=86400",
    );
  });

  it("does not store a lifetime of zero with no stale window", () => {
    const response = shaped({ status: 200, headers: { "cache-control": "s-maxage=0" } });
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
  });

  it("does not store a Cache-Control that names no lifetime", () => {
    const response = shaped({ status: 200, headers: { "cache-control": "public" } });
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("does not store a lifetime it cannot read", () => {
    const response = shaped({ status: 200, headers: { "cache-control": "s-maxage=soon" } });
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("takes an origin's CDN-Cache-Control as its opt-in and keeps it off the client", () => {
    const response = shaped({
      status: 200,
      headers: { "cdn-cache-control": "max-age=30, stale-while-revalidate=300" },
    });
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=30, stale-while-revalidate=300, stale-if-error=86400",
    );
    expect(response.headers.get("cdn-cache-control")).toBeNull();
    expect(response.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
  });

  it("tags a stored response with its release and its path, and prefixes the origin's tags", () => {
    const response = shaped({
      status: 200,
      headers: { ...stored, "cache-tag": `${release}|posts,other` },
    });
    expect(response.headers.get("cache-tag")?.split(",")).toEqual([
      release,
      `${release}|path:${pathname}`,
      `${release}|posts`,
      `${release}|other`,
    ]);
  });

  it("tags a stored response that carried no tags with the release and its path", () => {
    const response = shaped({ status: 200, headers: stored });
    expect(response.headers.get("cache-tag")).toBe(`${release},${release}|path:${pathname}`);
  });

  it("keeps at most 1000 tags, the release's two first", () => {
    const many = Array.from({ length: 1500 }, (_, i) => `t${i}`).join(",");
    const tags = shaped({ status: 200, headers: { ...stored, "cache-tag": many } })
      .headers.get("cache-tag")
      ?.split(",");
    expect(tags).toHaveLength(1000);
    expect(tags?.slice(0, 2)).toEqual([release, `${release}|path:${pathname}`]);
  });

  it("does not tag a response it does not store", () => {
    const response = shaped({
      status: 200,
      headers: { "cache-control": "private", "cache-tag": "a" },
    });
    expect(response.headers.get("cache-tag")).toBeNull();
  });

  it("drops Vary, because the variant is in the cache key already", () => {
    const response = shaped({
      status: 200,
      headers: { ...stored, vary: "rsc, next-router-state-tree" },
    });
    expect(response.headers.get("vary")).toBeNull();
  });

  it("keeps the ETag so a refresh can end in a 304", () => {
    const response = shaped({ status: 200, headers: { ...stored, etag: '"abc"' } });
    expect(response.headers.get("etag")).toBe('"abc"');
  });

  it("keeps the body", async () => {
    const response = shaped({ status: 200, headers: stored });
    expect(await response.text()).toBe("page");
  });
});
