import { getNextHost } from "./host.mjs";
import { type InstanceCache, instanceCacheBytes, newInstanceCache } from "./instance-cache.mjs";
import { clockMethods, tagClock } from "./tag-clock.mjs";
import { bufferValue, type CacheEntry, now, pendingSets, streamOf } from "./use-cache-entry.mjs";

interface StoredEntry {
  bytes: Uint8Array;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

const keyPrefix = "use-cache-default:";

const pending = pendingSets();

let cache: InstanceCache | undefined;

function instanceCache(): InstanceCache {
  cache ??= getNextHost().instanceCache ?? newInstanceCache(instanceCacheBytes(undefined));
  return cache;
}

const handler = {
  async get(cacheKey: string, _softTags: string[]): Promise<CacheEntry | undefined> {
    try {
      await pending.wait(cacheKey);

      const stored = instanceCache().read<StoredEntry>(keyPrefix + cacheKey);
      if (!stored) return undefined;
      if (now() > stored.timestamp + stored.revalidate * 1000) return undefined;
      if (tagClock.areTagsExpired(stored.tags, stored.timestamp)) return undefined;

      return {
        value: streamOf(stored.bytes),
        tags: stored.tags,
        stale: stored.stale,
        timestamp: stored.timestamp,
        expire: stored.expire,
        revalidate: tagClock.areTagsStale(stored.tags, stored.timestamp) ? -1 : stored.revalidate,
      };
    } catch {
      return undefined;
    }
  },

  async set(cacheKey: string, pendingEntry: Promise<CacheEntry>): Promise<void> {
    await pending.run(cacheKey, async () => {
      try {
        const entry = await pendingEntry;
        const bytes = await bufferValue(entry);
        if (!bytes) return;

        const stored: StoredEntry = {
          bytes,
          tags: entry.tags,
          stale: entry.stale,
          timestamp: entry.timestamp,
          expire: entry.expire,
          revalidate: entry.revalidate,
        };
        instanceCache().write(keyPrefix + cacheKey, stored, bytes.byteLength);
      } catch {}
    });
  },

  ...clockMethods,
};

export default handler;
