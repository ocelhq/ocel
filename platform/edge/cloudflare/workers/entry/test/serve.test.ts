import { createExecutionContext, env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import { Serve } from "../src/serve";
import { objectCall, type ServeProps } from "../src/serve-key";
import { FN_URL, withGlobalFetch } from "./origin-deps";

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
  return new Request(objectCall(host, forKey).url);
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

  it.each([403, 404])("keeps an object S3 answers %i for as a 404 for a minute", async (status) => {
    const s3 = s3Answering(() => new Response("<Error/>", { status }));

    const response = await withGlobalFetch(s3.fetch, () => serveWith(s3Env).fetch(get()));

    expect(response.status).toBe(404);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=60");
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

  it("falls back to the edge's object store for a release the bucket does not hold", async () => {
    await env.TAG_SNAPSHOT_STORE.put(key, "from r2");
    const s3 = s3Answering(() => new Response("<Error/>", { status: 404 }));

    const response = await withGlobalFetch(s3.fetch, () =>
      serveWith({ ...s3Env, OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE }).fetch(get()),
    );

    expect(s3.calls).toHaveLength(1);
    expect(await response.text()).toBe("from r2");
  });

  it("does not fall back from a failure of the bucket", async () => {
    const s3 = s3Answering(() => new Response("slow down", { status: 503 }));

    const response = await withGlobalFetch(s3.fetch, () =>
      serveWith({ ...s3Env, OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE }).fetch(get()),
    );

    expect(response.status).toBe(502);
  });

  it("answers only GET and HEAD", async () => {
    const response = await serveWith(s3Env).fetch(
      new Request(objectCall("h", key).url, { method: "POST", body: "x" }),
    );
    expect(response.status).toBe(405);
  });
});

describe("Serve reading an object where the origin has no asset bucket", () => {
  const r2Env = (): Partial<Env> => ({ OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE });

  it("reads the edge's own object store", async () => {
    await env.TAG_SNAPSHOT_STORE.put(key, "stored");

    const response = await serveWith(r2Env()).fetch(get());

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("stored");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=31536000");
  });

  it("keeps a missing object as a 404 for a minute", async () => {
    const response = await serveWith(r2Env()).fetch(get(`${key}.missing`));

    expect(response.status).toBe(404);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe("max-age=60");
  });

  it("is unavailable, and never stored, when there is nowhere to read from", async () => {
    const response = await serveWith({}).fetch(get());

    expect(response.status).toBe(503);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });
});

describe("Serve fetching from the origin", () => {
  const originProps: ServeProps = { ...props, kind: "origin" };
  const originEnv: Partial<Env> = {
    OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
    OCEL_EDGE_SECRET_KEY: "secretkey",
  };

  it("stamps the headers of what the origin answers", async () => {
    const wire = s3Answering(
      () =>
        new Response("page", {
          headers: { "cache-control": "s-maxage=15, stale-while-revalidate=60", vary: "rsc" },
        }),
    );

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(new Request(`${FN_URL}blog/a`)),
    );

    expect(wire.calls[0].url).toBe(`${FN_URL}blog/a`);
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=15, stale-while-revalidate=60, stale-if-error=86400",
    );
    expect(response.headers.get("vary")).toBeNull();
    expect(response.headers.get("cache-tag")).toBe("r1a2b3c4d,r1a2b3c4d|path:/blog/a");
  });

  it("makes a header-less page private", async () => {
    const wire = s3Answering(() => new Response("page"));

    const response = await withGlobalFetch(wire.fetch, () =>
      serveWith(originEnv, originProps).fetch(new Request(`${FN_URL}blog/a`)),
    );

    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });
});
