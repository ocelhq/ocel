import type { CacheEntryFile } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import type { InstanceCache } from "@framework/next-runtime/instance-cache";
import type { UseCacheEntry, UseCacheStore } from "@framework/next-runtime/use-cache-store";

const bytesPerCharacter = 2;

function writeEntry(cache: InstanceCache, key: string, entry: unknown): void {
  const json = JSON.stringify(entry);
  cache.write(key, json, json.length * bytesPerCharacter);
}

function readEntry<T>(cache: InstanceCache, key: string): T | null {
  const json = cache.read<string>(key);
  return json === undefined ? null : (JSON.parse(json) as T);
}

export function newInstanceCacheStore(cache: InstanceCache): CacheStore {
  return {
    async readEntry(key) {
      return readEntry<CacheEntryFile>(cache, `entry:${key}`);
    },
    async writeEntry(key, entry) {
      writeEntry(cache, `entry:${key}`, entry);
    },
    async readFetch(hash) {
      return readEntry<CacheEntryFile>(cache, `fetch:${hash}`);
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
      return readEntry<UseCacheEntry>(cache, `use-cache:${key}`);
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
