import { expect, test } from "vitest";
import { newCloudStorage } from "../src/next/cloud-storage.mjs";
import {
  newCloudStorageAssetBucket,
  newCloudStorageObjectStore,
} from "../src/next/cloud-storage-assets.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";

const release = "prod/shop/web/r1a2b3c4d";

function open() {
  const bucket = newCloudStorageBucket();
  const storage = newCloudStorage({
    bucket: "ocel-acme-production",
    endpoint: "http://storage.test",
    fetch: bucket.fetch,
  });
  const seed = (name: string, body: string | Uint8Array) =>
    bucket.objects.set(name, { body, generation: "7" });
  return { bucket, storage, seed };
}

test("an asset is read from the assets store under the key the router names", async () => {
  const { storage, seed } = open();
  seed(`assets/${release}/assets/_next/static/app.js`, "chunk");

  const object = await newCloudStorageAssetBucket(storage).get(
    `${release}/assets/_next/static/app.js`,
  );

  expect(await new Response(object?.body).text()).toBe("chunk");
});

test("an asset carries the generation Cloud Storage serves it at as its etag", async () => {
  const { storage, seed } = open();
  seed(`assets/${release}/assets/logo.svg`, "<svg/>");

  const object = await newCloudStorageAssetBucket(storage).get(`${release}/assets/logo.svg`);

  expect(object?.httpEtag).toBe('"7"');
});

test("an asset the store does not hold is a miss", async () => {
  const { storage } = open();

  expect(await newCloudStorageAssetBucket(storage).get(`${release}/assets/gone.js`)).toBeNull();
});

test("an object store hands the image optimizer the bytes of an original", async () => {
  const { storage, seed } = open();
  seed(`assets/${release}/assets/logo.png`, new Uint8Array([137, 80, 78, 71]));

  const stored = await newCloudStorageObjectStore(storage).get(`${release}/assets/logo.png`, 100);

  expect([...(stored?.bytes ?? [])]).toEqual([137, 80, 78, 71]);
});

test("an object store refuses an original larger than the limit it was given", async () => {
  const { storage, seed } = open();
  seed(`assets/${release}/assets/big.png`, new Uint8Array(50));

  await expect(
    newCloudStorageObjectStore(storage).get(`${release}/assets/big.png`, 10),
  ).rejects.toThrow(/50 bytes/);
});

test("an object store reports an absent original as undefined", async () => {
  const { storage } = open();

  expect(
    await newCloudStorageObjectStore(storage).get(`${release}/image-config.json`, 10),
  ).toBeUndefined();
});
