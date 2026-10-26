import { cacheKey } from "@framework/next-cache";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import {
  newTagSnapshotBucket,
  serveS3From,
  type TagSnapshotBucket,
} from "./tag-snapshot-bucket.mjs";

const coordinate = "proj/prod/app/r3f8a1c9d";
const snapshotKey = `tag-clock/${coordinate}/tag-clock.json`;

let bucket: TagSnapshotBucket;
let tagItems: string[];

beforeEach(() => {
  vi.resetModules();
  vi.stubEnv("OCEL_EDGE_BUCKET", "edge");
  vi.stubEnv("OCEL_CACHE_BUCKET", "cache");
  vi.stubEnv("OCEL_STORAGE_COORDINATE", coordinate);
  vi.stubEnv("OCEL_STATE_TABLE", "state");
  vi.stubEnv("OCEL_ISR_TAG_NAMESPACE", "PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#");
  bucket = newTagSnapshotBucket(snapshotKey, {
    version: 1,
    deployedAt: 100,
    generatedAt: 100,
    records: {},
  });
  tagItems = [];
  serveS3From(() => bucket);
  vi.doMock("@aws-sdk/client-dynamodb", async (orig) => ({
    ...(await orig<any>()),
    DynamoDBClient: class {
      async send(cmd: any) {
        tagItems.push(cmd.input.Key.pk.S);
        return {};
      }
    },
  }));
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.doUnmock("@aws-sdk/client-s3");
  vi.doUnmock("@aws-sdk/client-dynamodb");
});

async function newTask() {
  const { newAwsContainerNextHost } = await import("../src/next/container-next-host.mjs");
  return newAwsContainerNextHost();
}

function taggedPage(tag: string, lastModified: number) {
  return {
    lastModified,
    value: {
      kind: "APP_PAGE",
      html: `<p>${tag}</p>`,
      headers: { "x-next-cache-tags": tag },
      status: 200,
    },
  };
}

test("a page one task writes to the cache is the page another task reads", async () => {
  const writer = await (await newTask()).newCacheStore!();
  const reader = await (await newTask()).newCacheStore!();

  await writer.writeEntry(cacheKey("/products"), taggedPage("products", 1_000));

  expect(await reader.readEntry(cacheKey("/products"))).toEqual(taggedPage("products", 1_000));
});

test("a tag one task revalidates stops another task serving the page that carries it", async () => {
  const revalidating = await newTask();
  const pages = await revalidating.newCacheStore!();
  await pages.writeEntry(cacheKey("/products"), taggedPage("products", 1_000));
  await pages.writeEntry(cacheKey("/cart"), taggedPage("cart", 1_000));
  const at = Date.now();
  await (await revalidating.newUseCacheStore!()).writeTag("products", {
    expired: at,
    writtenAt: at,
  });

  const { installNextHost } = await import("@framework/next-runtime/host");
  const { setTagClockStore } = await import("@framework/next-runtime/tag-clock");
  const { default: CacheHandler } = await import("@framework/next-runtime/cache-handler");
  installNextHost(await newTask());
  setTagClockStore(undefined);
  const serving = new CacheHandler({ _requestHeaders: {} });

  expect(await serving.get("/products", { kind: "APP_PAGE" })).toBeNull();
  expect(await serving.get("/cart", { kind: "APP_PAGE" })).toMatchObject({ lastModified: 1_000 });
  expect(tagItems).toEqual(["PROJECT#proj#STACK#prod--app--r3f8a1c9d#TAG#products"]);
});
