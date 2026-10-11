import type http from "node:http";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { serveInvoke, setRequestScope } from "../src/host.mjs";
import { useOriginCache } from "../src/origin-cache.mjs";

const hostKey = Symbol.for("ocel.cache.host");

interface CacheHost {
  tag(tags: readonly string[]): void;
  revalidateTag(tag: string, mode: "expire" | "stale"): void;
  revalidatePath(path: string): void;
}

function cacheHost(): CacheHost {
  return (globalThis as Record<symbol, CacheHost>)[hostKey]!;
}

const rules = JSON.stringify([
  { path: "/blog/*", revalidate: 60, expire: 600 },
  { path: "/", revalidate: 30 },
]);

const behindCloudflare = {
  OCEL_ORIGIN_FRESH_ONLY: "1",
  OCEL_CACHE_TAG_PURGE: "1",
  OCEL_RELEASE_TOKEN: "r0a1b2c3d",
  OCEL_EDGE_ASKS_TO_CACHE: "1",
  OCEL_CACHE_RULES: rules,
};

const behindCloudFront = {
  OCEL_ORIGIN_FRESH_ONLY: "1",
  OCEL_CACHE_TAG_PURGE: "1",
  OCEL_RELEASE_TOKEN: "r0a1b2c3d",
  OCEL_CACHE_RULES: rules,
};

beforeEach(() => {
  delete (globalThis as Record<symbol, unknown>)[hostKey];
});

afterEach(() => {
  useOriginCache({});
  setRequestScope(undefined);
  vi.restoreAllMocks();
});

type Handler = (req: http.IncomingMessage, res: http.ServerResponse) => void | Promise<void>;

async function serve(
  env: Record<string, string>,
  handler: Handler,
): Promise<(path: string, init?: RequestInit) => Promise<Response>> {
  useOriginCache(env);
  let port = 0;
  await serveInvoke(handler, (listening) => {
    port = listening;
  });
  return (path, init) => fetch(`http://127.0.0.1:${port}${path}`, init);
}

const asked = { "x-ocel-edge-cache": "1" };

function page(req: http.IncomingMessage, res: http.ServerResponse): void {
  res.writeHead(200, { "content-type": "text/html" });
  res.end("page");
}

describe("an origin behind the Cloudflare gateway", () => {
  test("gives a response the rule for its path, and tags it for the gateway only when asked", async () => {
    const get = await serve(behindCloudflare, page);

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
    expect(res.headers.get("x-ocel-cache-tag")).toBe("r0a1b2c3d|path:/blog/a");
    expect(res.headers.has("cache-tag")).toBe(false);
  });

  test("shapes nothing for a request the gateway did not ask to cache", async () => {
    const get = await serve(behindCloudflare, page);

    const res = await get("/blog/a");

    expect(res.headers.has("cache-control")).toBe(false);
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
    expect(res.headers.has("cache-tag")).toBe(false);
  });

  test("tags a response with what its handler tagged, stamped by the release", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().tag(["posts", "post:1"]);
      page(req, res);
    });

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("x-ocel-cache-tag")).toBe(
      "r0a1b2c3d|posts,r0a1b2c3d|post:1,r0a1b2c3d|path:/blog/a",
    );
  });

  test("leaves a response that sets its own s-maxage as it set it", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      res.writeHead(200, { "cache-control": "public, s-maxage=5" });
      res.end("page");
    });

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("cache-control")).toBe("public, s-maxage=5");
    expect(res.headers.get("x-ocel-cache-tag")).toBe("r0a1b2c3d|path:/blog/a");
  });

  test("keeps the browser lifetime a response declares and adds the rule's beside it", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      res.writeHead(200, { "cache-control": "public, max-age=10" });
      res.end("page");
    });

    const res = await get("/", { headers: asked });

    expect(res.headers.get("cache-control")).toBe("public, max-age=10, s-maxage=30");
  });

  test("leaves a personal response untagged and unshaped", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().tag(["posts"]);
      res.writeHead(200, { "cache-control": "private, no-store" });
      res.end("mine");
    });

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
  });

  test("leaves a response that sets a cookie, and one that is not a 200 or a read, unshaped", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      if (req.method === "POST") res.writeHead(200);
      else if (req.url === "/blog/cookie") res.writeHead(200, { "set-cookie": "a=1" });
      else res.writeHead(404);
      res.end();
    });

    for (const res of [
      await get("/blog/cookie", { headers: asked }),
      await get("/blog/missing", { headers: asked }),
      await get("/blog/a", { method: "POST", headers: asked }),
    ]) {
      expect(res.headers.has("cache-control")).toBe(false);
      expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
    }
  });

  test("tags nothing for a path no rule names and a response with no s-maxage", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().tag(["posts"]);
      page(req, res);
    });

    const res = await get("/about", { headers: asked });

    expect(res.headers.has("cache-control")).toBe(false);
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
  });

  test("announces a tag revalidated while handling a request, stale or expired", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "stale");
      cacheHost().revalidateTag("post:1", "expire");
      cacheHost().revalidatePath("/blog/a");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/blog/a", { method: "POST", headers: asked });

    expect(res.headers.get("x-ocel-purge")).toBe(
      "purge r0a1b2c3d|post:1,r0a1b2c3d|path:/blog/a invalidate r0a1b2c3d|posts",
    );
  });

  test("announces a raise on a request the gateway did not ask to cache", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "expire");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.get("x-ocel-purge")).toBe("purge r0a1b2c3d|posts");
  });

  test("announces an expiry over a stale marking of the same tag", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "expire");
      cacheHost().revalidateTag("posts", "stale");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.get("x-ocel-purge")).toBe("purge r0a1b2c3d|posts");
  });

  test("refuses a tag outside a request, where there is no response to carry it", async () => {
    await serve(behindCloudflare, page);

    expect(() => cacheHost().tag(["posts"])).toThrow(/request/);
    expect(() => cacheHost().revalidateTag("posts", "expire")).toThrow(/request/);
  });

  test("warns that a raise made after the response began reaches nothing", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const get = await serve(behindCloudflare, (req, res) => {
      res.writeHead(200);
      cacheHost().revalidateTag("posts", "expire");
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/posts.*before the response/));
  });
});

describe("an origin behind CloudFront or the load balancer", () => {
  test("shapes every cacheable response and writes its tags to cache-tag", async () => {
    const get = await serve(behindCloudFront, (req, res) => {
      cacheHost().tag(["posts"]);
      page(req, res);
    });

    const res = await get("/blog/a");

    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
    expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|posts,r0a1b2c3d|path:/blog/a");
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
  });

  test("announces no purge, and says once that a raise reaches nothing here", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const get = await serve(behindCloudFront, (req, res) => {
      cacheHost().revalidateTag("posts", "expire");
      res.writeHead(200);
      res.end();
    });

    const first = await get("/api/publish", { method: "POST" });
    await get("/api/publish", { method: "POST" });

    expect(first.headers.has("x-ocel-purge")).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/posts.*Cloudflare/));
  });
});

describe("an origin behind no caching edge", () => {
  test("shapes nothing, and exposes no cache host for the SDK to call", async () => {
    const get = await serve({ OCEL_CACHE_RULES: rules }, page);

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.has("cache-control")).toBe(false);
    expect(res.headers.has("cache-tag")).toBe(false);
    expect((globalThis as Record<symbol, unknown>)[hostKey]).toBeUndefined();
  });
});
