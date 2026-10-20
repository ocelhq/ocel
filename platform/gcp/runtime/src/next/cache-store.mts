import { type CacheEntryFile, entryObjectKey } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import {
  objectKey,
  pagesPrefix,
  type StorageCoordinate,
} from "@platform/edge-contract/storage-key";
import type { CloudStorage } from "./cloud-storage.mjs";

export type CacheBuckets = { edge: CloudStorage; cache: CloudStorage };

export function newGcpCacheStore(storage: CacheBuckets, coordinate: StorageCoordinate): CacheStore {
  const pages = pagesPrefix(coordinate);
  const entryName = (key: string) => {
    const name = entryObjectKey(pages, key);
    if (name === null) {
      throw new Error(
        `ocel cache handler: cache key ${JSON.stringify(key)} is not addressable inside ${pages}`,
      );
    }
    return name;
  };
  const fetchName = (hash: string) => objectKey("fetch-cache", coordinate, `${hash}.cache.json`);

  async function read(bucket: CloudStorage, name: string): Promise<CacheEntryFile | null> {
    const object = await bucket.read(name);
    return object.status === "found" ? JSON.parse(object.body) : null;
  }

  async function write(bucket: CloudStorage, name: string, entry: CacheEntryFile): Promise<void> {
    await bucket.write(name, JSON.stringify(entry));
  }

  return {
    readEntry: async (key) => read(storage.edge, entryName(key)),
    writeEntry: async (key, entry) => write(storage.edge, entryName(key), entry),
    readFetch: (hash) => read(storage.cache, fetchName(hash)),
    writeFetch: (hash, entry) => write(storage.cache, fetchName(hash), entry),
  };
}
