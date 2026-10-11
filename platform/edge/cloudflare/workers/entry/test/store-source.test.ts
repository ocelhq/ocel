import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import { openEntrySource } from "../src/store-source";
import { withGlobalFetch } from "./origin-deps";

const key = "prod/p1/web/r9source0/isr/cache/blog/a.cache.json";

const awsBucket = {
  OCEL_ASSET_BUCKET: "assets",
  OCEL_AWS_REGION: "eu-west-2",
  OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
  OCEL_EDGE_SECRET_KEY: "secretkey",
} as unknown as Env;

const hmacBucket = {
  OCEL_ASSET_STORE_ENDPOINT: "https://storage.googleapis.com",
  OCEL_ASSET_STORE_BUCKET: "tier-bucket",
  OCEL_ASSET_STORE_PREFIX: "assets/",
  OCEL_ASSET_STORE_ACCESS_KEY_ID: "GOOGEXAMPLE",
  OCEL_ASSET_STORE_SECRET_KEY: "secretkey",
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
  it("reads the AWS asset bucket", async () => {
    const bucket = bucketAnswering(() => new Response("from the bucket"));

    const read = await withGlobalFetch(bucket.fetch, async () =>
      (await openEntrySource(awsBucket)?.get(key))?.text(),
    );

    expect(read).toBe("from the bucket");
    expect(bucket.asked).toEqual([`https://assets.s3.eu-west-2.amazonaws.com/${key}`]);
  });

  it("finds no entry the bucket does not hold, and reads nowhere else", async () => {
    const bucket = bucketAnswering(() => new Response(null, { status: 404 }));

    const read = await withGlobalFetch(bucket.fetch, async () =>
      openEntrySource(awsBucket)?.get(key),
    );

    expect(read).toBeNull();
    expect(bucket.asked).toHaveLength(1);
  });

  it("is not opened when the edge holds no key to a bucket", () => {
    expect(openEntrySource({} as Env)).toBeUndefined();
  });

  it("is not opened on the Google tier bucket, which keeps no page entry the edge may read", () => {
    expect(openEntrySource(hmacBucket)).toBeUndefined();
  });
});
