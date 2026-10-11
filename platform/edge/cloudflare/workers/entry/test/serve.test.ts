import { createExecutionContext } from "cloudflare:test";
import { describe, expect, it, vi } from "vitest";
import type { Env } from "../src/env";
import { Serve } from "../src/serve";
import {
  buildEdgeCall,
  buildObjectCall,
  buildOriginCall,
  buildServeRequest,
  ORIGIN_PURGE_SCOPE,
  RENDER_RELEASE_HEADER,
  SERVE_REFUSED_HEADER,
  type ServeProps,
} from "../src/serve-key";
import { FN_URL, withGlobalFetch } from "./origin-deps";
import { expectedSignature, sentSignature } from "./sigv4";

const key = "prod/shop/web/r1a2b3c4d/assets/_next/static/app.js";
const props: ServeProps = {
  kind: "object",
  host: "shop.example.com",
  app: "web",
  release: "r1a2b3c4d",
};

const s3Env: Partial<Env> = {
  OCEL_ASSET_BUCKET: "assets-bucket",
  OCEL_AWS_REGION: "us-east-1",
  OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
  OCEL_EDGE_SECRET_KEY: "secretkey",
};

function serveWith(over: Partial<Env>, forProps: ServeProps = props): Serve {
  const ctx = Object.assign(createExecutionContext(), { props: forProps });
  return new Serve(ctx, { ...over } as Env);
}

function get(forKey = key, host = props.host): Request {
  return new Request(buildObjectCall(host, forKey).url);
}

function s3Answering(answer: () => Response): { calls: Request[]; fetch: typeof fetch } {
  const calls: Request[] = [];
  return {
    calls,
    fetch: (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(new Request(input as RequestInfo, init));
      return answer();
    }) as typeof fetch,
  };
}

describe("Serve reading an object from the asset bucket", () => {
  it("signs a GET of the key against the bucket with the edge's key", async () => {
    const s3 = s3Answering(() => new Response("bytes", { headers: { etag: '"e1"' } }));

    const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

    expect(s3.calls).toHaveLength(1);
    expect(s3.calls[0].url).toBe(
      "https://assets-bucket.s3.us-east-1.amazonaws.com/prod/shop/web/r1a2b3c4d/assets/_next/static/app.js",
    );
    expect(s3.calls[0].headers.get("authorization")).toMatch(
      /^AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE\/.+\/us-east-1\/s3\//,
    );
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("bytes");
    expect(response.headers.get("etag")).toBe('"e1"');
  });

  it("lets the edge keep the bytes for a year, because their key names the release", async () => {
    const s3 = s3Answering(() => new Response("bytes"));

    const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=31536000");
    expect(response.headers.get("cache-control")).toBe("public, max-age=0, must-revalidate");
    expect(response.headers.get("cache-tag")).toBe("r1a2b3c4d");
  });

  it("keeps an object S3 answers 404 for as a 404 for a minute", async () => {
    const s3 = s3Answering(() => new Response("<Error/>", { status: 404 }));

    const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

    expect(response.status).toBe(404);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=60");
  });

  it("never stores, and reports, an object S3 refuses the edge's key", async () => {
    const s3 = s3Answering(() => new Response("<Error>AccessDenied</Error>", { status: 403 }));
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    try {
      const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

      expect(response.status).toBe(502);
      expect(response.headers.get("cache-control")).toBe("private, no-store");
      expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
      expect(logged).toHaveBeenCalledWith(
        expect.stringMatching(new RegExp(`403.*${key}|${key}.*403`)),
      );
    } finally {
      logged.mockRestore();
    }
  });

  it("never stores a failure of the bucket", async () => {
    const s3 = s3Answering(() => new Response("slow down", { status: 503 }));

    const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

    expect(response.status).toBe(502);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
  });

  it("reads the bucket named by a key's own release only", async () => {
    const s3 = s3Answering(() => new Response("bytes"));
    const other = key.replace("r1a2b3c4d", "r5e6f7a8b");

    await expect(
      withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get(other))),
    ).rejects.toThrow(/release/);
    expect(s3.calls).toHaveLength(0);
  });

  it("reads only the app its props name", async () => {
    const s3 = s3Answering(() => new Response("bytes"));
    const other = key.replace("/web/", "/admin/");

    await expect(
      withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get(other))),
    ).rejects.toThrow(/app/);
  });

  it("takes the bucket's 404 as final", async () => {
    const s3 = s3Answering(() => new Response("<Error/>", { status: 404 }));

    const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

    expect(s3.calls).toHaveLength(1);
    expect(response.status).toBe(404);
  });

  it("reads nothing when the bucket is bound without the edge's key", async () => {
    const s3 = s3Answering(() => new Response("bytes"));

    const response = await withGlobalFetch(s3.fetch, () =>
      serveWith({ OCEL_ASSET_BUCKET: "assets-bucket", OCEL_AWS_REGION: "us-east-1" }).fetch(get()),
    );

    expect(s3.calls).toHaveLength(0);
    expect(response.status).toBe(503);
  });

  it("answers only GET and HEAD", async () => {
    const response = await serveWith(s3Env).fetch(
      new Request(buildObjectCall("h", key).url, { method: "POST", body: "x" }),
    );
    expect(response.status).toBe(405);
  });
});

describe("Serve reading an object from a bucket an HMAC key reads", () => {
  const hmacEnv: Partial<Env> = {
    OCEL_ASSET_STORE_ENDPOINT: "https://objects.example.com/",
    OCEL_ASSET_STORE_BUCKET: "ocel-acme-production",
    OCEL_ASSET_STORE_PREFIX: "assets/",
    OCEL_ASSET_STORE_ACCESS_KEY_ID: "GOOG1EXAMPLEACCESSID",
    OCEL_ASSET_STORE_SECRET_KEY: "examplesecret/key+",
  };
  const routeTable = "prod/shop/web/r1a2b3c4d/route-table/ab.json";

  it("is a path-style GET of the key under the store's prefix at the endpoint the origin names", async () => {
    const gcs = s3Answering(() => new Response("bytes", { headers: { etag: '"e1"' } }));

    const response = await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get()));

    expect(gcs.calls).toHaveLength(1);
    expect(gcs.calls[0].method).toBe("GET");
    expect(gcs.calls[0].url).toBe(`https://objects.example.com/ocel-acme-production/assets/${key}`);
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("bytes");
    expect(response.headers.get("etag")).toBe('"e1"');
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=31536000");
  });

  it("reads a release's route table from the same store", async () => {
    const gcs = s3Answering(() => new Response("{}"));

    await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get(routeTable)));

    expect(gcs.calls[0].url).toBe(
      `https://objects.example.com/ocel-acme-production/assets/${routeTable}`,
    );
  });

  it("signs the GET as Signature Version 4 in the region auto for the service s3", async () => {
    const gcs = s3Answering(() => new Response("bytes"));

    await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get()));

    const authorization = gcs.calls[0].headers.get("authorization") ?? "";
    expect(authorization).toMatch(
      /^AWS4-HMAC-SHA256 Credential=GOOG1EXAMPLEACCESSID\/\d{8}\/auto\/s3\/aws4_request, /,
    );
    expect(sentSignature(gcs.calls[0])).toBe(
      await expectedSignature(gcs.calls[0], "examplesecret/key+"),
    );
  });

  it("encodes the characters of an object name once, in the url and in the signature", async () => {
    const gcs = s3Answering(() => new Response("page"));
    const odd = "prod/shop/web/r1a2b3c4d/assets/docs/[...slug] one.html";

    await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get(odd)));

    expect(new URL(gcs.calls[0].url).pathname).toBe(
      "/ocel-acme-production/assets/prod/shop/web/r1a2b3c4d/assets/docs/%5B...slug%5D%20one.html",
    );
    expect(sentSignature(gcs.calls[0])).toBe(
      await expectedSignature(gcs.calls[0], "examplesecret/key+"),
    );
  });

  it("never sends the secret", async () => {
    const gcs = s3Answering(() => new Response("bytes"));

    await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get()));

    expect([...gcs.calls[0].headers.values()].join(" ")).not.toContain("examplesecret");
    expect(gcs.calls[0].url).not.toContain("examplesecret");
  });

  it("takes the bucket's 404 as final", async () => {
    const gcs = s3Answering(() => new Response("<Error/>", { status: 404 }));

    const response = await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get()));

    expect(gcs.calls).toHaveLength(1);
    expect(response.status).toBe(404);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=60");
  });

  it("never stores, and reports, an object the bucket refuses the key", async () => {
    const gcs = s3Answering(() => new Response("<Error>AccessDenied</Error>", { status: 403 }));
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    try {
      const response = await withGlobalFetch(gcs.fetch, () => serveWith(hmacEnv).fetch(get()));

      expect(response.status).toBe(502);
      expect(response.headers.get("cache-control")).toBe("private, no-store");
      expect(logged).toHaveBeenCalledWith(
        expect.stringMatching(/ocel-acme-production.*403|403.*ocel-acme-production/),
      );
    } finally {
      logged.mockRestore();
    }
  });

  it("reads nothing when the origin names no endpoint", async () => {
    const gcs = s3Answering(() => new Response("bytes"));

    const response = await withGlobalFetch(gcs.fetch, () =>
      serveWith({ ...hmacEnv, OCEL_ASSET_STORE_ENDPOINT: undefined }).fetch(get()),
    );

    expect(gcs.calls).toHaveLength(0);
    expect(response.status).toBe(503);
  });
});

describe("Serve reading an object where the origin has no asset bucket", () => {
  it("is unavailable, and never stored, when there is nowhere to read from", async () => {
    const response = await serveWith({}).fetch(get());

    expect(response.status).toBe(503);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });
});

describe("Serve fetching from the origin", () => {
  const originProps: ServeProps = ORIGIN_PURGE_SCOPE;
  const coordinates = { host: "shop.example.com", app: "web", release: "r1a2b3c4d" };
  const isrPrefix = "prod/p1/web/r1a2b3c4d/isr";
  const renderRelease = { [RENDER_RELEASE_HEADER]: JSON.stringify({ slug: "p1", isrPrefix }) };
  const originRequest = (path: string, origin = FN_URL) =>
    new Request(buildOriginCall(`${origin}${path}`, coordinates).url, { headers: renderRelease });
  const servedRecords = (record: object | null) => ({
    readServedRecord: async () => (record ? { kind: "record", record } : { kind: "not-served" }),
  });
  const originRecord = {
    app: "web",
    framework: "next",
    release: "build-1~fp1",
    buildId: "build-1",
    functionUrls: { "/blog": `${FN_URL}stage` },
    assetPrefix: "prod/p1/web/r1a2b3c4d/assets",
    isrPrefix,
    createdAt: 1,
  };
  const originEnv = {
    OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
    OCEL_EDGE_SECRET_KEY: "secretkey",
    RELEASES: servedRecords(originRecord),
  } as unknown as Partial<Env>;

  it("fetches nothing from an origin that is not one of the release's functions", async () => {
    const wire = s3Answering(
      () => new Response("page", { headers: { "cache-control": "s-maxage=15" } }),
    );

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(
        originRequest("blog/a", "https://elsewhere.example/"),
      ),
    );

    expect(wire.calls).toEqual([]);
    expect(response.status).toBe(503);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get(SERVE_REFUSED_HEADER)).toBe("1");
  });

  it("fetches nothing for a call that names no release", async () => {
    const wire = s3Answering(
      () => new Response("page", { headers: { "cache-control": "s-maxage=15" } }),
    );

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(
        new Request(buildOriginCall(`${FN_URL}blog/a`, coordinates).url),
      ),
    );

    expect(wire.calls).toEqual([]);
    expect(response.headers.get(SERVE_REFUSED_HEADER)).toBe("1");
  });

  it("fetches nothing for a release that is not served, or is not the one the call is keyed under", async () => {
    for (const record of [null, { ...originRecord, isrPrefix: "prod/p1/web/r9z8y7x6w/isr" }]) {
      const wire = s3Answering(() => new Response("page"));

      const response = await withGlobalFetch(wire.fetch, () =>
        serveWith(
          { ...originEnv, RELEASES: servedRecords(record) } as Partial<Env>,
          originProps,
        ).fetch(originRequest("blog/a")),
      );

      expect(wire.calls).toEqual([]);
      expect(response.headers.get(SERVE_REFUSED_HEADER)).toBe("1");
    }
  });

  it("stamps the headers of what the origin answers", async () => {
    const wire = s3Answering(
      () =>
        new Response("page", {
          headers: { "cache-control": "s-maxage=15, stale-while-revalidate=60", vary: "rsc" },
        }),
    );

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(originRequest("blog/a")),
    );

    expect(wire.calls[0].url).toBe(`${FN_URL}blog/a`);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=15, stale-while-revalidate=60, stale-if-error=86400",
    );
    expect(response.headers.get("vary")).toBeNull();
    expect(response.headers.get("cache-tag")).toBe("r1a2b3c4d,r1a2b3c4d|path:/blog/a");
  });

  it("stores what the origin answers under the tags it named in x-ocel-cache-tag, which a fetch through Cloudflare does not strip", async () => {
    const wire = s3Answering(
      () =>
        new Response("page", {
          headers: {
            "cache-control": "s-maxage=3600",
            "x-ocel-cache-tag": "r1a2b3c4d|_N_T_/blog/a,r1a2b3c4d|posts",
          },
        }),
    );

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(originRequest("blog/a")),
    );

    expect(response.headers.get("cache-tag")?.split(",")).toEqual([
      "r1a2b3c4d",
      "r1a2b3c4d|path:/blog/a",
      "r1a2b3c4d|_N_T_/blog/a",
      "r1a2b3c4d|posts",
    ]);
    expect(response.headers.has("x-ocel-cache-tag")).toBe(false);
  });

  it("fetches the origin URL it was called with, as the visitor's host and without the visitor's credentials", async () => {
    const wire = s3Answering(
      () => new Response("page", { headers: { "cache-control": "s-maxage=15" } }),
    );
    const visitor = new Request("https://shop.example.com/blog/a?page=2", {
      headers: { cookie: "session=1", authorization: "Bearer t", "if-none-match": '"e1"' },
    });
    const call = buildOriginCall(`${FN_URL}blog/a?page=2`, coordinates);
    const sent = buildServeRequest(call.url, visitor);
    sent.headers.set(RENDER_RELEASE_HEADER, renderRelease[RENDER_RELEASE_HEADER]);

    await withGlobalFetch(wire.fetch, () => serveWith(originEnv, call.props).fetch(sent));

    expect(wire.calls).toHaveLength(1);
    expect(wire.calls[0].url).toBe(`${FN_URL}blog/a?page=2`);
    expect(wire.calls[0].headers.get("x-forwarded-host")).toBe("shop.example.com");
    expect(wire.calls[0].headers.get("x-forwarded-proto")).toBe("https");
    expect(wire.calls[0].headers.get("cookie")).toBeNull();
    expect(wire.calls[0].headers.get("if-none-match")).toBe('"e1"');
    expect(wire.calls[0].headers.get("authorization")).toMatch(
      /^AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE\//,
    );
  });

  it("fetches the origin its call names, whatever host the runtime puts in the URL it hands Serve", async () => {
    const wire = s3Answering(
      () => new Response("page", { headers: { "cache-control": "s-maxage=15" } }),
    );
    const call = buildOriginCall(`${FN_URL}blog/a?page=2`, coordinates);
    const rehosted = new URL(call.url);
    rehosted.host = "shop.example.com";

    await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, call.props).fetch(new Request(rehosted, { headers: renderRelease })),
    );

    expect(wire.calls.map(({ url }) => url)).toEqual([`${FN_URL}blog/a?page=2`]);
  });

  it("makes a header-less page private", async () => {
    const wire = s3Answering(() => new Response("page"));

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(originRequest("blog/a")),
    );

    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("hands the vary it drops to the gateway as x-ocel-vary", async () => {
    const wire = s3Answering(
      () =>
        new Response("page", {
          headers: { "cache-control": "s-maxage=15", vary: "rsc, next-router-state-tree" },
        }),
    );

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(originRequest("blog/a")),
    );

    expect(response.headers.get("x-ocel-vary")).toBe("rsc, next-router-state-tree");
  });

  describe("a segment prefetch", () => {
    const segmentRequest = () => {
      const request = originRequest("blog/a");
      request.headers.set("rsc", "1");
      request.headers.set("next-router-segment-prefetch", "/_tree");
      return request;
    };

    it("stores nothing the origin answers that is not a segment", async () => {
      const wire = s3Answering(
        () =>
          new Response("<postponed shell>", {
            headers: { "cache-control": "s-maxage=15", "x-nextjs-postponed": "1" },
          }),
      );

      const response = await withGlobalFetch(wire.fetch, () =>
        serveWith(originEnv, originProps).fetch(segmentRequest()),
      );

      expect(response.headers.get("cache-control")).toBe("private, no-store");
      expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
    });

    it("stores the segment the origin answers", async () => {
      const wire = s3Answering(
        () =>
          new Response("segment", {
            headers: { "cache-control": "s-maxage=15", "x-nextjs-postponed": "2" },
          }),
      );

      const response = await withGlobalFetch(wire.fetch, () =>
        serveWith(originEnv, originProps).fetch(segmentRequest()),
      );

      expect(response.headers.get("cloudflare-cdn-cache-control")).toContain("max-age=15");
    });
  });

  describe("an origin response announcing a purge", () => {
    function serveRaising(raised: { mode: string; tags: { tag: string }[] }[]): {
      serve: Serve;
      settled: () => Promise<void>;
    } {
      const pending: Promise<unknown>[] = [];
      const ctx = Object.assign(createExecutionContext(), {
        props: originProps,
        waitUntil: (promise: Promise<unknown>) => void pending.push(promise),
        exports: {
          PurgeQueue: {
            getByName: () => ({
              raise: async (mode: string, tags: { tag: string }[]) => {
                raised.push({ mode, tags });
              },
              record: async () => {},
            }),
          },
        },
      });
      return {
        serve: new Serve(ctx as never, { ...originEnv } as Env),
        settled: async () => void (await Promise.all(pending)),
      };
    }

    const announcing = () =>
      s3Answering(
        () =>
          new Response("page", {
            headers: {
              "cache-control": "s-maxage=15",
              "x-ocel-purge": "purge r1a2b3c4d|a invalidate r1a2b3c4d|b",
            },
          }),
      );

    it("never stores the announcement with the page", async () => {
      const raised: { mode: string; tags: { tag: string }[] }[] = [];
      const { serve, settled } = serveRaising(raised);

      const response = await withGlobalFetch(announcing().fetch, () =>
        serve.fetch(originRequest("blog/a")),
      );
      await settled();

      expect(response.headers.has("x-ocel-purge")).toBe(false);
      expect(response.headers.get("cloudflare-cdn-cache-control")).toContain("max-age=15");
    });

    it("purges the announced tags behind the response, and queues what the cache cannot take", async () => {
      const raised: { mode: string; tags: { tag: string }[] }[] = [];
      const { serve, settled } = serveRaising(raised);

      await withGlobalFetch(announcing().fetch, () => serve.fetch(originRequest("blog/a")));
      await settled();

      expect(raised.map(({ mode, tags }) => [mode, tags.map(({ tag }) => tag)])).toEqual([
        ["purge", ["r1a2b3c4d|a"]],
        ["invalidate", ["r1a2b3c4d|b"]],
      ]);
    });
  });
});

describe("Serve rendering an edge-runtime prerender", () => {
  const coordinates = { host: "shop.example.com", app: "web", release: "r1a2b3c4d" };
  const isrPrefix = "prod/p1/web/r1a2b3c4d/isr";
  const bundleKey = "prod/p1/web/r1a2b3c4d/edge/bundle.json";
  const edgeRecord = {
    app: "web",
    framework: "next",
    release: "build-1~fp1",
    buildId: "build-1",
    functionUrls: {},
    assetPrefix: "prod/p1/web/r1a2b3c4d/assets",
    isrPrefix,
    createdAt: 1,
    purgeSecret: "s3cr3t",
    edgeWorkers: { bundleKey, id: "w1", compatDate: "2026-03-10" },
  };

  function edgeServe(answer: () => Response, record: object | null = edgeRecord) {
    const rendered: { entryKey: string; request: Request }[] = [];
    const served: { slug: string; isrPrefix: string }[] = [];
    const loader = {
      get: () => ({
        getEntrypoint: (_name: unknown, options: { props: { entryKey: string } }) => ({
          fetch: async (request: Request) => {
            rendered.push({ entryKey: options.props.entryKey, request });
            return answer();
          },
        }),
      }),
    };
    const releases = {
      readServedRecord: async (args: { slug: string; isrPrefix: string }) => {
        served.push(args);
        return record ? { kind: "record", record } : { kind: "not-served" };
      },
    };
    const ctx = Object.assign(createExecutionContext(), {
      props: ORIGIN_PURGE_SCOPE,
      exports: { Serve: () => ({ fetch: async () => new Response("{}") }) },
    });
    const serve = new Serve(
      ctx as never,
      { ...s3Env, LOADER: loader, RELEASES: releases } as unknown as Env,
    );
    return { serve, rendered, served };
  }

  function edgeRequest(path: string, tags: string[] = ["posts"]): Request {
    const call = buildEdgeCall(`https://shop.example.com${path}`, coordinates, "app/edge-blog");
    return new Request(call.url, {
      headers: {
        [RENDER_RELEASE_HEADER]: JSON.stringify({ slug: "p1", isrPrefix, tags }),
        "x-ocel-entry": "app/edge-blog/page",
      },
    });
  }

  const cacheable = () =>
    new Response("<html>edge</html>", { headers: { "cache-control": "s-maxage=60" } });

  it("renders the entry through the release's edge bundle at the public URL", async () => {
    const { serve, rendered, served } = edgeServe(cacheable);

    const response = await serve.fetch(edgeRequest("/blog?page=2"));

    expect(served).toEqual([{ slug: "p1", isrPrefix }]);
    expect(rendered.map(({ entryKey }) => entryKey)).toEqual(["app/edge-blog"]);
    expect(rendered[0].request.url).toBe("https://shop.example.com/blog?page=2");
    expect(rendered[0].request.headers.has(RENDER_RELEASE_HEADER)).toBe(false);
    expect(rendered[0].request.headers.get("x-ocel-entry")).toBe("app/edge-blog/page");
    expect(await response.text()).toBe("<html>edge</html>");
  });

  it("stores the render under the release, its path and the route's tags", async () => {
    const { serve } = edgeServe(cacheable);

    const response = await serve.fetch(edgeRequest("/blog", ["posts", "authors"]));

    expect(response.headers.get("cloudflare-cdn-cache-control")).toContain("max-age=60");
    expect(response.headers.get("cache-tag")?.split(",")).toEqual([
      "r1a2b3c4d",
      "r1a2b3c4d|path:/blog",
      "r1a2b3c4d|posts",
      "r1a2b3c4d|authors",
    ]);
  });

  it("stores nothing the edge render did not mark storable", async () => {
    const { serve } = edgeServe(() => new Response("<html>edge</html>"));

    const response = await serve.fetch(edgeRequest("/blog"));

    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("stores nothing it renders for a segment prefetch that is not a segment", async () => {
    const { serve } = edgeServe(
      () =>
        new Response("<postponed shell>", {
          headers: { "cache-control": "s-maxage=60", "x-nextjs-postponed": "1" },
        }),
    );
    const request = edgeRequest("/blog");
    request.headers.set("rsc", "1");
    request.headers.set("next-router-segment-prefetch", "/_tree");

    const response = await serve.fetch(request);

    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
  });

  it("renders nothing for a release that is no longer served", async () => {
    const { serve, rendered } = edgeServe(cacheable, null);

    const response = await serve.fetch(edgeRequest("/blog"));

    expect(rendered).toEqual([]);
    expect(response.status).toBe(503);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("renders nothing from a record of another release than the one it is keyed under", async () => {
    const { serve, rendered } = edgeServe(cacheable, {
      ...edgeRecord,
      isrPrefix: "prod/p1/web/r9z8y7x6w/isr",
    });

    const response = await serve.fetch(edgeRequest("/blog"));

    expect(rendered).toEqual([]);
    expect(response.status).toBe(503);
  });
});
