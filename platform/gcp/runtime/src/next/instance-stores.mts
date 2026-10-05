import type { CacheEntryFile } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import type { InstanceCache } from "@framework/next-runtime/instance-cache";
import type { UseCacheEntry, UseCacheStore } from "@framework/next-runtime/use-cache-store";

const bytesPerCharacter = 2;

function writeEntry(cache: InstanceCache, key: string, entry: unknown): void {
  cache.write(key, entry, JSON.stringify(entry).length * bytesPerCharacter);
}

export function newInstanceCacheStore(cache: InstanceCache): CacheStore {
  return {
    async readEntry(key) {
      return cache.read<CacheEntryFile>(`entry:${key}`) ?? null;
    },
    async writeEntry(key, entry) {
      writeEntry(cache, `entry:${key}`, entry);
    },
    async readFetch(hash) {
      return cache.read<CacheEntryFile>(`fetch:${hash}`) ?? null;
    },
    async writeFetch(hash, entry) {
      writeEntry(cache, `fetch:${hash}`, entry);
    },
    async writeTags() {},
  };
}

export function newInstanceUseCacheStore(cache: InstanceCache): UseCacheStore {
  return {
    async readEntry(key) {
      return cache.read<UseCacheEntry>(`use-cache:${key}`) ?? null;
    },
    async writeEntry(key, entry) {
      writeEntry(cache, `use-cache:${key}`, entry);
    },
    async readTagSnapshot() {
      return { status: "fresh", records: {}, etag: null };
    },
    async writeTag() {
      return true;
    },
  };
}
