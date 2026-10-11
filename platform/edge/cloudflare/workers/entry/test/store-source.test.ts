import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import { openEntrySource } from "../src/store-source";
import { withGlobalFetch } from "./origin-deps";

const key = "prod/p1/web/r9source0/isr/cache/blog/a.cache.json";

const bothStores = {
  OCEL_ASSET_BUCKET: "assets",
  OCEL_AWS_REGION: "eu-west-2",
  OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
  OCEL_EDGE_SECRET_KEY: "secretkey",
  OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE,
} as unknown as Env;

function bucketAnswering(answer: () => Response) {
  const asked: string[] = [];
  const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    asked.push(new Request(input as RequestInfo, init).url);
    return answer();
  }) as typeof globalThis.fetch;
  return { asked, fetch };
}

describe("the store a Serve miss reads page entries from", () => {
  it("reads the asset bucket first", async () => {
    await env.TAG_SNAPSHOT_STORE.put(key, "from r2");
    const bucket = bucketAnswering(() => new Response("from the bucket"));

    const read = await withGlobalFetch(bucket.fetch, async () =>
      (await openEntrySource(bothStores)?.get(key))?.text(),
    );

    expect(read).toBe("from the bucket");
    expect(bucket.asked).toEqual([`https://assets.s3.eu-west-2.amazonaws.com/${key}`]);
  });

  it("reads the bound R2 store when the asset bucket has no such entry", async () => {
    await env.TAG_SNAPSHOT_STORE.put(key, "from r2");
    const bucket = bucketAnswering(() => new Response(null, { status: 404 }));

    const read = await withGlobalFetch(bucket.fetch, async () =>
      (await openEntrySource(bothStores)?.get(key))?.text(),
    );

    expect(read).toBe("from r2");
  });

  it("reads the R2 store alone when the edge holds no key to a bucket", async () => {
    await env.TAG_SNAPSHOT_STORE.put(key, "from r2");

    const read = await (
      await openEntrySource({ OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE } as unknown as Env)?.get(
        key,
      )
    )?.text();

    expect(read).toBe("from r2");
  });
});
