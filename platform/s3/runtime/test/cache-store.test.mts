import type { S3Client } from "@aws-sdk/client-s3";
import { expect, test } from "vitest";
import { newS3CacheStore } from "../src/cache-store.mjs";

const coordinate = { project: "shop", env: "prod", app: "web", release: "r1a2b3c4d" };

function fakeBucket() {
  const objects = new Map<string, string>();
  const sent: { command: string; input: any }[] = [];
  const client = {
    async send(command: any) {
      const name = command.constructor.name;
      sent.push({ command: name, input: command.input });
      const key = `${command.input.Bucket}/${command.input.Key}`;
      if (name === "PutObjectCommand") {
        objects.set(key, command.input.Body);
        return {};
      }
      const body = objects.get(key);
      if (body === undefined) {
        throw Object.assign(new Error("NoSuchKey"), {
          name: "NoSuchKey",
          $metadata: { httpStatusCode: 404 },
        });
      }
      return { Body: { transformToString: async () => body } };
    },
  } as unknown as S3Client;
  return { client, sent };
}

function storeOver(bucket: ReturnType<typeof fakeBucket>) {
  return newS3CacheStore({
    pages: { client: bucket.client, bucket: "edge" },
    fetches: { client: bucket.client, bucket: "cache" },
    coordinate,
  });
}

const entry = { lastModified: 1, value: { kind: "APP_PAGE", html: "<html>hi</html>" } };

test("a page entry is kept in the pages bucket under the release's pages prefix", async () => {
  const bucket = fakeBucket();
  const store = storeOver(bucket);

  await store.writeEntry("blog/first", entry);

  expect(bucket.sent[0]).toMatchObject({
    command: "PutObjectCommand",
    input: {
      Bucket: "edge",
      Key: "pages/shop/prod/web/r1a2b3c4d/blog/first.cache.json",
      ContentType: "application/json",
    },
  });
  expect(await store.readEntry("blog/first")).toEqual(entry);
});

test("a fetch entry is kept in the fetches bucket under the release's fetch prefix", async () => {
  const bucket = fakeBucket();
  const store = storeOver(bucket);

  await store.writeFetch("5f1c", entry);

  expect(bucket.sent[0]?.input).toMatchObject({
    Bucket: "cache",
    Key: "fetch-cache/shop/prod/web/r1a2b3c4d/5f1c.cache.json",
  });
  expect(await store.readFetch("5f1c")).toEqual(entry);
});

test("an entry the bucket does not hold is a miss", async () => {
  const store = storeOver(fakeBucket());

  expect(await store.readEntry("absent")).toBeNull();
  expect(await store.readFetch("absent")).toBeNull();
});

test("a key that would address something outside the release's pages is refused", async () => {
  const store = storeOver(fakeBucket());

  await expect(store.readEntry("../other/index")).rejects.toThrow("not addressable");
});
