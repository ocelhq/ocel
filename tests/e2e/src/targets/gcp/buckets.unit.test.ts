import { afterEach, describe, expect, it } from "bun:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { repoRoot } from "../../paths";
import {
  appBucketPrefix,
  bucketsIn,
  deleteAppBucket,
  listAppBuckets,
  strayBuckets,
} from "./buckets";
import type { Where } from "./store";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

const where: Where = { endpoint: undefined, project: "p", region: "r", token: "t" };

function storageAnswering(answer: (url: URL, method: string) => Response): string[] {
  const asked: string[] = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    asked.push(`${method} ${url.pathname}${url.search}`);
    return answer(url, method);
  }) as typeof fetch;
  return asked;
}

describe("appBucketPrefix", () => {
  it("is the prefix the provider names a namespace's app buckets under", async () => {
    expect(appBucketPrefix("ocel")).toBe("ocel--");
    const go = await readFile(
      path.join(repoRoot, "platform", "gcp", "provider", "namespace.go"),
      "utf8",
    );
    expect(go).toContain('namespaceEnd        = "--"');
  });
});

describe("bucketsIn", () => {
  it("reads each bucket's name and the project it was labelled with", () => {
    expect(
      bucketsIn({
        items: [{ name: "ocel--j-1-prod-uploads-a1", labels: { "ocel-project": "j-1" } }],
      }),
    ).toEqual([{ name: "ocel--j-1-prod-uploads-a1", project: "j-1" }]);
    expect(bucketsIn({})).toEqual([]);
  });
});

describe("strayBuckets", () => {
  it("is what a run that died left under the harness's projects", () => {
    const buckets = [
      { name: "a", project: "j-1-sdk-node" },
      { name: "b", project: "j-2-sdk-node" },
      { name: "c", project: "customer-shop" },
    ];
    expect(strayBuckets(buckets, ["j-2-sdk-node"])).toEqual(["a"]);
  });
});

describe("listAppBuckets", () => {
  it("reads every page of the project's buckets under the prefix", async () => {
    const asked = storageAnswering((url) =>
      url.searchParams.get("pageToken") === "next"
        ? Response.json({ items: [{ name: "ocel--b", labels: { "ocel-project": "j-2" } }] })
        : Response.json({
            items: [{ name: "ocel--a", labels: { "ocel-project": "j-1" } }],
            nextPageToken: "next",
          }),
    );

    expect(await listAppBuckets(where, "ocel--")).toEqual([
      { name: "ocel--a", project: "j-1" },
      { name: "ocel--b", project: "j-2" },
    ]);
    expect(asked).toEqual([
      "GET /storage/v1/b?project=p&prefix=ocel--",
      "GET /storage/v1/b?project=p&prefix=ocel--&pageToken=next",
    ]);
  });
});

describe("deleteAppBucket", () => {
  it("deletes every object, then the bucket", async () => {
    const asked = storageAnswering((_, method) =>
      method === "GET"
        ? Response.json({ items: [{ name: "a/b.png" }] })
        : new Response(null, { status: 204 }),
    );

    await deleteAppBucket(where, "ocel--a");
    expect(asked).toEqual([
      "GET /storage/v1/b/ocel--a/o",
      "DELETE /storage/v1/b/ocel--a/o/a%2Fb.png",
      "DELETE /storage/v1/b/ocel--a",
    ]);
  });

  it("is done when the bucket is already gone", async () => {
    const asked = storageAnswering(() => Response.json({}, { status: 404 }));

    await deleteAppBucket(where, "ocel--a");
    expect(asked).toEqual(["GET /storage/v1/b/ocel--a/o"]);
  });

  it("fails naming what Cloud Storage refused", async () => {
    storageAnswering(() => new Response("denied", { status: 403 }));

    await expect(deleteAppBucket(where, "ocel--a")).rejects.toThrow("403 denied");
  });
});
