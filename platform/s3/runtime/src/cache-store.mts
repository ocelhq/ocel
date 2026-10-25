import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { type CacheEntryFile, entryObjectKey } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import {
  formatStorageCoordinate,
  type StorageCoordinate,
  objectKey as storageKey,
} from "@platform/edge-contract/storage-key";
import { isNotFound, type ObjectStore, readBodyText } from "./object-store.mjs";

export interface S3CacheLocation {
  pages: ObjectStore;
  fetches: ObjectStore;
  coordinate: StorageCoordinate;
}

export function newS3CacheStore({ pages, fetches, coordinate }: S3CacheLocation): CacheStore {
  const objectKey = (key: string) => {
    const addressed = entryObjectKey(coordinate, key);
    if (addressed === null) {
      throw new Error(
        `ocel cache handler: cache key ${JSON.stringify(key)} is not addressable inside the pages of ${formatStorageCoordinate(coordinate)}`,
      );
    }
    return addressed;
  };
  const fetchKey = (hash: string) => storageKey("fetch-cache", coordinate, `${hash}.cache.json`);

  async function read(from: ObjectStore, key: string): Promise<CacheEntryFile | null> {
    try {
      const out = await from.client.send(new GetObjectCommand({ Bucket: from.bucket, Key: key }));
      return JSON.parse(await readBodyText(out.Body));
    } catch (err: any) {
      if (isNotFound(err)) return null;
      throw err;
    }
  }

  async function write(to: ObjectStore, key: string, entry: CacheEntryFile): Promise<void> {
    await to.client.send(
      new PutObjectCommand({
        Bucket: to.bucket,
        Key: key,
        Body: JSON.stringify(entry),
        ContentType: "application/json",
      }),
    );
  }

  return {
    readEntry: async (key) => read(pages, objectKey(key)),
    writeEntry: async (key, entry) => write(pages, objectKey(key), entry),
    readFetch: (hash) => read(fetches, fetchKey(hash)),
    writeFetch: (hash, entry) => write(fetches, fetchKey(hash), entry),
  };
}
