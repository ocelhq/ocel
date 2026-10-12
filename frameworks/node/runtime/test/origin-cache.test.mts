import type http from "node:http";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { serveInvoke, setRequestScope } from "../src/host.mjs";
import { useOriginRouteTable } from "../src/origin-route-table.mjs";
import { setTagRecordWriter } from "../src/tag-records.mjs";
import { bypassToken, newRouteTable, writeRouteTable } from "./route-table-file.mjs";

const hostKey = Symbol.for("ocel.cache.host");

interface CacheHost {
  tag(tags: readonly string[]): void;
  revalidateTag(tag: string, mode: "purge" | "invalidate"): void;
  revalidatePath(path: string, mode: "purge" | "invalidate"): void;
}

function cacheHost(): CacheHost {
  return (globalThis as Record<symbol, CacheHost>)[hostKey]!;
}

const routes = writeRouteTable(newRouteTable());

const behindCloudflare = {
  OCEL_EDGE_CACHE: "marked",
  OCEL_CACHE_TAG_PURGE: "1",
  OCEL_RELEASE_TOKEN: "r0a1b2c3d",
  OCEL_ROUTE_TABLE: routes,
};

const behindCloudFront = {
  OCEL_EDGE_CACHE: "all",
  OCEL_CACHE_TAG_PURGE: "1",
  OCEL_RELEASE_TOKEN: "r0a1b2c3d",
  OCEL_ROUTE_TABLE: routes,
};

beforeEach(() => {
  delete (globalThis as Record<symbol, unknown>)[hostKey];
});

afterEach(() => {
  useOriginRouteTable({});
  setRequestScope(undefined);
  setTagRecordWriter(undefined);
  vi.restoreAllMocks();
});

type Handler = (req: http.IncomingMessage, res: http.ServerResponse) => void | Promise<void>;

async function serve(
  env: Record<string, string>,
  handler: Handler,
): Promise<(path: string, init?: RequestInit) => Promise<Response>> {
  useOriginRouteTable(env);
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
  test("gives a response the rule for its route, and tags it for the gateway only when asked", async () => {
    const get = await serve(behindCloudflare, page);

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
    expect(res.headers.get("x-ocel-cache-tag")).toBe("r0a1b2c3d|blog,r0a1b2c3d|path:/blog/a");
    expect(res.headers.has("cache-tag")).toBe(false);
  });

  test("shapes nothing for a request the gateway did not ask to cache", async () => {
    const get = await serve(behindCloudflare, page);

    const res = await get("/blog/a");

    expect(res.headers.has("cache-control")).toBe(false);
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
    expect(res.headers.has("cache-tag")).toBe(false);
  });

  test("tags a response with what its handler tagged beside what its route tags, stamped by the release", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().tag(["posts", "post:1"]);
      page(req, res);
    });

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("x-ocel-cache-tag")).toBe(
      "r0a1b2c3d|blog,r0a1b2c3d|posts,r0a1b2c3d|post:1,r0a1b2c3d|path:/blog/a",
    );
  });

  test("gives a response the shared lifetime of its route's rule over the one its handler set", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      res.writeHead(200, {
        "cache-control": "public, max-age=10, s-maxage=5, stale-while-revalidate=5",
        "cdn-cache-control": "max-age=600",
        "surrogate-control": "max-age=600",
      });
      res.end("page");
    });

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.get("cache-control")).toBe(
      "public, max-age=10, s-maxage=60, stale-while-revalidate=540",
    );
    expect(res.headers.has("cdn-cache-control")).toBe(false);
    expect(res.headers.has("surrogate-control")).toBe(false);
    expect(res.headers.get("x-ocel-cache-tag")).toBe("r0a1b2c3d|blog,r0a1b2c3d|path:/blog/a");
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

  test("tags nothing for a route that caches nothing and a response with no s-maxage", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().tag(["posts"]);
      page(req, res);
    });

    for (const path of ["/about", "/account"]) {
      const res = await get(path, { headers: asked });

      expect(res.headers.has("cache-control"), path).toBe(false);
      expect(res.headers.has("x-ocel-cache-tag"), path).toBe(false);
    }
  });

  test("announces a tag revalidated while handling a request, stale or expired", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "invalidate");
      cacheHost().revalidateTag("post:1", "purge");
      cacheHost().revalidatePath("/blog/a", "purge");
      cacheHost().revalidatePath("/blog/b", "invalidate");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/blog/a", { method: "POST", headers: asked });

    expect(res.headers.get("x-ocel-purge")).toBe(
      "purge r0a1b2c3d|post:1,r0a1b2c3d|path:/blog/a invalidate r0a1b2c3d|posts,r0a1b2c3d|path:/blog/b",
    );
  });

  test("names a path outside ASCII by the pathname the edge tags it with", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      if (req.method === "POST") cacheHost().revalidatePath("/blog/café", "purge");
      page(req, res);
    });

    const read = await get("/blog/café", { headers: asked });
    const write = await get("/api/publish", { method: "POST" });

    expect(read.headers.get("x-ocel-cache-tag")).toBe(
      "r0a1b2c3d|blog,r0a1b2c3d|path:/blog/caf%C3%A9",
    );
    expect(write.headers.get("x-ocel-purge")).toBe("purge r0a1b2c3d|path:/blog/caf%C3%A9");
  });

  test("warns when it revalidates a tag no edge can store", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("two words", "purge");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/two words/));
  });

  test("announces a revalidation on a request the gateway did not ask to cache", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "purge");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.get("x-ocel-purge")).toBe("purge r0a1b2c3d|posts");
  });

  test("announces an expiry over a stale marking of the same tag", async () => {
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "purge");
      cacheHost().revalidateTag("posts", "invalidate");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.get("x-ocel-purge")).toBe("purge r0a1b2c3d|posts");
  });

  test("refuses a tag outside a request, where there is no response to carry it", async () => {
    await serve(behindCloudflare, page);

    expect(() => cacheHost().tag(["posts"])).toThrow(/request/);
    expect(() => cacheHost().revalidateTag("posts", "purge")).toThrow(/request/);
  });

  test("warns that a revalidation made after the response began reaches nothing in band", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const get = await serve(behindCloudflare, (req, res) => {
      res.writeHead(200);
      cacheHost().revalidateTag("posts", "purge");
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });

    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/posts.*before the response/));
  });
});

describe("an origin behind CloudFront or the load balancer", () => {
  test("shapes every cacheable response of a cached route and writes its tags to cache-tag", async () => {
    const get = await serve(behindCloudFront, (req, res) => {
      cacheHost().tag(["posts"]);
      page(req, res);
    });

    const res = await get("/blog/a");

    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
    expect(res.headers.get("cache-tag")).toBe(
      "r0a1b2c3d|blog,r0a1b2c3d|posts,r0a1b2c3d|path:/blog/a",
    );
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
  });

  test("announces no purge in band, and says nothing of a revalidation it writes durably", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const written: unknown[] = [];
    setTagRecordWriter(async (entries) => {
      written.push(...entries);
    });
    const get = await serve(behindCloudFront, (req, res) => {
      cacheHost().revalidateTag("posts", "purge");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });
    await vi.waitFor(() => expect(written).toHaveLength(1));

    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(written).toEqual([{ tag: "posts", mode: "purge", raisedAt: expect.any(Number) }]);
    expect(warn).not.toHaveBeenCalled();
  });

  test("says once that a revalidation reaches no edge where nothing writes it durably", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const get = await serve(behindCloudFront, (req, res) => {
      cacheHost().revalidateTag("posts", "purge");
      res.writeHead(200);
      res.end();
    });

    const first = await get("/api/publish", { method: "POST" });
    await get("/api/publish", { method: "POST" });

    expect(first.headers.has("x-ocel-purge")).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith(expect.stringMatching(/posts.*reaches no edge/));
  });
});

describe("a revalidation behind the gateway that also writes durably", () => {
  test("announces it in band and writes each tag it names, a path by its path tag", async () => {
    const written: { tag: string; mode: string }[] = [];
    setTagRecordWriter(async (entries) => {
      written.push(...entries.map(({ tag, mode }) => ({ tag, mode })));
    });
    const get = await serve(behindCloudflare, (req, res) => {
      cacheHost().revalidateTag("posts", "invalidate");
      cacheHost().revalidatePath("/blog/a", "purge");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });
    await vi.waitFor(() => expect(written).toHaveLength(2));

    expect(res.headers.get("x-ocel-purge")).toBe(
      "purge r0a1b2c3d|path:/blog/a invalidate r0a1b2c3d|posts",
    );
    expect(written).toEqual([
      { tag: "posts", mode: "invalidate" },
      { tag: "path:/blog/a", mode: "purge" },
    ]);
  });

  test("keeps the invocation alive until the durable write settles", async () => {
    let settled = false;
    setTagRecordWriter(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
      settled = true;
    });
    const waited: Promise<unknown>[] = [];
    useOriginRouteTable(behindCloudFront);
    let port = 0;
    await serveInvoke(
      (req, res, ocel) => {
        const waitUntil = ocel.waitUntil;
        ocel.waitUntil = (task) => {
          waited.push(task);
          waitUntil(task);
        };
        cacheHost().revalidateTag("posts", "purge");
        res.writeHead(200);
        res.end();
      },
      (listening) => {
        port = listening;
      },
    );

    await fetch(`http://127.0.0.1:${port}/api/publish`, { method: "POST" });
    await Promise.all(waited);

    expect(settled).toBe(true);
  });

  test("logs a durable write that fails, and still answers the request", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    setTagRecordWriter(async () => {
      throw new Error("table unreachable");
    });
    const get = await serve(behindCloudFront, (req, res) => {
      cacheHost().revalidateTag("posts", "purge");
      res.writeHead(200);
      res.end();
    });

    const res = await get("/api/publish", { method: "POST" });
    await vi.waitFor(() =>
      expect(error).toHaveBeenCalledWith(expect.stringMatching(/table unreachable/)),
    );

    expect(res.status).toBe(200);
  });
});

describe("a request for a cached route", () => {
  test.each([
    ["behind CloudFront or the load balancer", behindCloudFront, {}],
    ["behind the Cloudflare gateway", behindCloudflare, asked],
  ])("reaches the handler without its cookie %s", async (_edge, env, headers) => {
    let seen: string | undefined = "unset";
    const get = await serve(env, (req, res) => {
      seen = req.headers.cookie;
      page(req, res);
    });

    const res = await get("/blog/a", { headers: { ...headers, cookie: "session=1" } });

    expect(seen).toBeUndefined();
    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
  });

  test("keeps the cookie of a write, and of a read of a route that caches nothing", async () => {
    const seen: Record<string, string | undefined> = {};
    const get = await serve(behindCloudFront, (req, res) => {
      seen[`${req.method} ${req.url}`] = req.headers.cookie;
      page(req, res);
    });

    await get("/blog/a", { method: "POST", headers: { cookie: "session=1" } });
    await get("/account", { headers: { cookie: "session=2" } });

    expect(seen).toEqual({ "POST /blog/a": "session=1", "GET /account": "session=2" });
  });

  test.each([
    ["behind CloudFront or the load balancer", behindCloudFront, {}],
    ["behind the Cloudflare gateway", behindCloudflare, asked],
  ])(
    "gets no shared lifetime from a rule when it carries credentials %s",
    async (_edge, env, headers) => {
      const get = await serve(env, page);

      const res = await get("/blog/a", { headers: { ...headers, authorization: "Bearer a" } });

      expect(res.headers.has("cache-control")).toBe(false);
      expect(res.headers.has("cache-tag")).toBe(false);
      expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
    },
  );

  test("is kept from every shared cache when it carries credentials, and keeps its browser lifetime", async () => {
    const get = await serve(behindCloudFront, (req, res) => {
      res.writeHead(200, { "cache-control": "public, max-age=10, s-maxage=5" });
      res.end("page");
    });

    const res = await get("/blog/a", { headers: { authorization: "Bearer a" } });

    expect(res.headers.get("cache-control")).toBe("private, max-age=10");
    expect(res.headers.has("cache-tag")).toBe(false);
  });

  test("replaces the s-maxage its handler set with its route's rule, and tags it", async () => {
    const get = await serve(behindCloudFront, (req, res) => {
      res.writeHead(200, {
        "cache-control": "public, s-maxage=5",
        "cdn-cache-control": "max-age=600",
        "surrogate-control": "max-age=600",
      });
      res.end("page");
    });

    const res = await get("/blog/a", { headers: { cookie: "session=1" } });

    expect(res.headers.get("cache-control")).toBe(
      "public, s-maxage=60, stale-while-revalidate=540",
    );
    expect(res.headers.has("cdn-cache-control")).toBe(false);
    expect(res.headers.has("surrogate-control")).toBe(false);
    expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|blog,r0a1b2c3d|path:/blog/a");
  });
});

describe("a request carrying the revalidate token", () => {
  const revalidating = { "x-prerender-revalidate": bypassToken };

  test.each([
    ["behind CloudFront or the load balancer", behindCloudFront],
    ["behind the Cloudflare gateway", behindCloudflare],
  ])("is rendered fresh and answered private and unstored %s", async (_edge, env) => {
    let rendered = 0;
    const get = await serve(env, (req, res) => {
      rendered++;
      page(req, res);
    });

    const res = await get("/blog/a", { headers: { ...asked, ...revalidating } });

    expect(rendered).toBe(1);
    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(res.headers.has("cache-tag")).toBe(false);
    expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
  });

  test("writes a purge of its path for CloudFront or the load balancer to clear", async () => {
    const written: { tag: string; mode: string }[] = [];
    setTagRecordWriter(async (entries) => {
      written.push(...entries.map(({ tag, mode }) => ({ tag, mode })));
    });
    const get = await serve(behindCloudFront, page);

    await get("/blog/a", { headers: revalidating });
    await vi.waitFor(() => expect(written).toHaveLength(1));

    expect(written).toEqual([{ tag: "path:/blog/a", mode: "purge" }]);
  });

  test("leaves the clearing to the Cloudflare gateway, which purges the path itself", async () => {
    const written: string[] = [];
    setTagRecordWriter(async (entries) => {
      written.push(...entries.map(({ tag }) => tag));
    });
    const get = await serve(behindCloudflare, page);

    const res = await get("/blog/a", { headers: revalidating });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(res.headers.has("x-ocel-purge")).toBe(false);
    expect(written).toEqual([]);
  });

  test("is a plain request for the cache when its token is wrong", async () => {
    const written: string[] = [];
    setTagRecordWriter(async (entries) => {
      written.push(...entries.map(({ tag }) => tag));
    });
    const get = await serve(behindCloudFront, page);

    const res = await get("/blog/a", { headers: { "x-prerender-revalidate": "wrong" } });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
    expect(written).toEqual([]);
  });
});

describe("a request bypassing the cache", () => {
  const bypass = { cookie: `theme=dark; __prerender_bypass=${bypassToken}` };

  test.each([
    ["behind CloudFront or the load balancer", behindCloudFront, {}],
    ["behind the Cloudflare gateway", behindCloudflare, asked],
  ])(
    "reaches the handler with its cookie and is answered private and unstored %s",
    async (_edge, env, headers) => {
      let seen: string | undefined;
      const get = await serve(env, (req, res) => {
        seen = req.headers.cookie;
        cacheHost().tag(["posts"]);
        page(req, res);
      });

      const res = await get("/blog/a", { headers: { ...headers, ...bypass } });

      expect(seen).toBe(bypass.cookie);
      expect(res.headers.get("cache-control")).toBe("private, no-store");
      expect(res.headers.has("cache-tag")).toBe(false);
      expect(res.headers.has("x-ocel-cache-tag")).toBe(false);
    },
  );

  test("replaces the lifetime the handler gave its response, which no edge may store", async () => {
    const get = await serve(behindCloudFront, (req, res) => {
      res.writeHead(200, { "cache-control": "public, s-maxage=5" });
      res.end("page");
    });

    const res = await get("/blog/a", { headers: bypass });

    expect(res.headers.get("cache-control")).toBe("private, no-store");
  });

  test("is not bypassing when its cookie holds another token", async () => {
    const get = await serve(behindCloudFront, page);

    const res = await get("/blog/a", { headers: { cookie: "__prerender_bypass=wrong" } });

    expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
  });

  test("leaves a route that caches nothing as the handler answered it", async () => {
    const get = await serve(behindCloudFront, page);

    const res = await get("/account", { headers: bypass });

    expect(res.headers.has("cache-control")).toBe(false);
  });
});

describe("a response of a route that caches nothing", () => {
  test.each([
    ["behind CloudFront or the load balancer", behindCloudFront, {}],
    ["behind the Cloudflare gateway", behindCloudflare, asked],
  ])(
    "reaches the edge with the cache headers its handler set, on a route or the fallback %s",
    async (_edge, env, headers) => {
      const get = await serve(env, (req, res) => {
        res.writeHead(200, {
          "cache-control": "public, s-maxage=60",
          "cdn-cache-control": "max-age=600",
          "surrogate-control": "max-age=600",
        });
        res.end("shared");
      });

      for (const path of ["/account", "/about"]) {
        const res = await get(path, { headers });

        expect(res.headers.get("cache-control"), path).toBe("public, s-maxage=60");
        expect(res.headers.get("cdn-cache-control"), path).toBe("max-age=600");
        expect(res.headers.get("surrogate-control"), path).toBe("max-age=600");
      }
    },
  );
});

describe("a response of a route that caches nothing, which names a lifetime the edge takes", () => {
  test.each([
    ["behind CloudFront or the load balancer", behindCloudFront, "cache-tag", {}],
    ["behind the Cloudflare gateway", behindCloudflare, "x-ocel-cache-tag", asked],
  ])(
    "is tagged by the tags its handler named, and its path, %s",
    async (_edge, env, header, headers) => {
      const get = await serve(env, (_req, res) => {
        cacheHost().tag(["posts"]);
        res.writeHead(200, { "cache-control": "public, max-age=60" });
        res.end("page");
      });

      const res = await get("/account", { headers });

      expect(res.headers.get("cache-control")).toBe("public, max-age=60");
      expect(res.headers.get(header)).toBe("r0a1b2c3d|posts,r0a1b2c3d|path:/account");
    },
  );

  test("is tagged for a CDN-Cache-Control lifetime too", async () => {
    const get = await serve(behindCloudFront, (_req, res) => {
      res.writeHead(200, { "cdn-cache-control": "max-age=60" });
      res.end("page");
    });

    const res = await get("/account");

    expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|path:/account");
  });

  test.each([
    ["a max-age of zero it may serve stale", "public, max-age=0, stale-while-revalidate=60"],
    ["an s-maxage of zero it may serve stale", "s-maxage=0, stale-while-revalidate=60"],
  ])("is tagged for %s, which an edge stores", async (_name, cacheControl) => {
    const get = await serve(behindCloudFront, (_req, res) => {
      res.writeHead(200, { "cache-control": cacheControl });
      res.end("page");
    });

    const res = await get("/account");

    expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|path:/account");
  });

  test("is tagged for a Cloudflare-CDN-Cache-Control lifetime", async () => {
    const get = await serve(behindCloudFront, (_req, res) => {
      res.writeHead(200, { "cloudflare-cdn-cache-control": "max-age=60" });
      res.end("page");
    });

    const res = await get("/account");

    expect(res.headers.get("cache-tag")).toBe("r0a1b2c3d|path:/account");
  });

  test.each([
    ["a max-age of zero", "public, max-age=0"],
    ["an s-maxage of zero, which wins over a max-age", "s-maxage=0, max-age=60"],
    ["a response that is private", "private, max-age=60"],
    ["a response that names no lifetime", "public"],
  ])("is not tagged for %s", async (_name, cacheControl) => {
    const get = await serve(behindCloudFront, (_req, res) => {
      res.writeHead(200, { "cache-control": cacheControl });
      res.end("page");
    });

    const res = await get("/account");

    expect(res.headers.has("cache-tag")).toBe(false);
  });
});

describe("an origin behind no caching edge", () => {
  test("shapes nothing, and exposes no cache host for the SDK to call", async () => {
    const get = await serve({ OCEL_ROUTE_TABLE: routes }, page);

    const res = await get("/blog/a", { headers: asked });

    expect(res.headers.has("cache-control")).toBe(false);
    expect(res.headers.has("cache-tag")).toBe(false);
    expect((globalThis as Record<symbol, unknown>)[hostKey]).toBeUndefined();
  });
});
