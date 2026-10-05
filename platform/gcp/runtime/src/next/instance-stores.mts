import type { CacheEntryFile } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import type { UseCacheEntry, UseCacheStore } from "@framework/next-runtime/use-cache-store";

class BoundedEntries<T> {
  private readonly entries = new Map<string, { value: T; bytes: number }>();
  private usedBytes = 0;

  constructor(private readonly maxBytes: number) {}

  read(key: string): T | null {
    const held = this.entries.get(key);
    if (!held) return null;
    this.entries.delete(key);
    this.entries.set(key, held);
    return held.value;
  }

  write(key: string, value: T): void {
    this.drop(key);
    const bytes = JSON.stringify(value).length;
    if (bytes > this.maxBytes) return;
    this.entries.set(key, { value, bytes });
    this.usedBytes += bytes;
    while (this.usedBytes > this.maxBytes) {
      const oldest = this.entries.keys().next().value;
      if (oldest === undefined) break;
      this.drop(oldest);
    }
  }

  private drop(key: string): void {
    const held = this.entries.get(key);
    if (!held) return;
    this.usedBytes -= held.bytes;
    this.entries.delete(key);
  }
}

export function newInstanceCacheStore(maxBytes: number): CacheStore {
  const held = new BoundedEntries<CacheEntryFile>(maxBytes);
  return {
    async readEntry(key) {
      return held.read(`entry:${key}`);
    },
    async writeEntry(key, entry) {
      held.write(`entry:${key}`, entry);
    },
    async readFetch(hash) {
      return held.read(`fetch:${hash}`);
    },
    async writeFetch(hash, entry) {
      held.write(`fetch:${hash}`, entry);
    },
    async writeTags() {},
  };
}

export function newInstanceUseCacheStore(maxBytes: number): UseCacheStore {
  const held = new BoundedEntries<UseCacheEntry>(maxBytes);
  return {
    async readEntry(key) {
      return held.read(key);
    },
    async writeEntry(key, entry) {
      held.write(key, entry);
    },
    async readTagSnapshot() {
      return { status: "fresh", records: {}, etag: null };
    },
    async writeTag() {
      return true;
    },
  };
}
