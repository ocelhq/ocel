import type { OriginStore } from "@platform/edge-contract/origin-reach";
import { describe, expect, it } from "vitest";
import { openStore } from "../../src/origin/store";
import { withGlobalFetch } from "../origin-deps";

const key = "pages/p1/prod/web/r9a0c0e00/blog/a.cache.json";

const awsBucket: OriginStore = {
  url: "https://assets.s3.eu-west-2.amazonaws.com",
  region: "eu-west-2",
  holdsEntries: true,
  accessKeyId: "AKIAEXAMPLE",
  secretAccessKey: "secretkey",
};

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
      (await openStore(awsBucket).entries?.get(key))?.text(),
    );

    expect(read).toBe("from the bucket");
    expect(bucket.asked).toEqual([`https://assets.s3.eu-west-2.amazonaws.com/${key}`]);
  });

  it("finds no entry the bucket answers 404 for, and reads nowhere else", async () => {
    const bucket = bucketAnswering(() => new Response(null, { status: 404 }));

    const read = await withGlobalFetch(bucket.fetch, async () =>
      openStore(awsBucket).entries?.get(key),
    );

    expect(read).toBeNull();
    expect(bucket.asked).toHaveLength(1);
  });

  it("fails a read the bucket refuses the edge's key with 403, rather than finding no entry", async () => {
    const bucket = bucketAnswering(() => new Response(null, { status: 403 }));

    await expect(
      withGlobalFetch(bucket.fetch, async () => openStore(awsBucket).entries?.get(key)),
    ).rejects.toThrow(`403 for https://assets.s3.eu-west-2.amazonaws.com/${key}`);
  });

  it("fails a read the bucket answers with an error, naming where it read", async () => {
    const bucket = bucketAnswering(() => new Response(null, { status: 500 }));

    await expect(
      withGlobalFetch(bucket.fetch, async () => openStore(awsBucket).entries?.get(key)),
    ).rejects.toThrow(`500 for https://assets.s3.eu-west-2.amazonaws.com/${key}`);
  });

  it("is not opened on a store that keeps no page entry the edge may read", () => {
    expect(openStore({ ...awsBucket, holdsEntries: false }).entries).toBeUndefined();
  });
});
