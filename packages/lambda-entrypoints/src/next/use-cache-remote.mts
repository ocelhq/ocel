import { clockMethods, tagClock, useCacheStore } from "./tag-clock.mjs";
import {
  bufferValue,
  now,
  pendingSets,
  streamOf,
  type CacheEntry,
} from "./use-cache-entry.mjs";

const pending = pendingSets();

const handler = {
  async get(cacheKey: string, _softTags: string[]): Promise<CacheEntry | undefined> {
    try {
      await pending.wait(cacheKey);

      if (!tagClock.hasSynced) return undefined;

      const store = useCacheStore();
      if (!store) return undefined;

      const stored = await store.readEntry(cacheKey);
      if (!stored) return undefined;

      if (now() > stored.timestamp + stored.expire * 1000) return undefined;
      if (tagClock.areTagsExpired(stored.tags, stored.timestamp)) return undefined;

      return {
        value: streamOf(Buffer.from(stored.body, "base64")),
        tags: stored.tags,
        stale: stored.stale,
        timestamp: stored.timestamp,
        expire: stored.expire,
        revalidate: tagClock.areTagsStale(stored.tags, stored.timestamp)
          ? -1
          : stored.revalidate,
      };
    } catch {
      return undefined;
    }
  },

  async set(cacheKey: string, pendingEntry: Promise<CacheEntry>): Promise<void> {
    await pending.run(cacheKey, async () => {
      try {
        const store = useCacheStore();
        if (!store) return;

        const entry = await pendingEntry;
        const bytes = await bufferValue(entry);
        if (!bytes) return;

        await store.writeEntry(cacheKey, {
          tags: entry.tags,
          stale: entry.stale,
          timestamp: entry.timestamp,
          expire: entry.expire,
          revalidate: entry.revalidate,
          body: Buffer.from(bytes).toString("base64"),
        });
      } catch {
      }
    });
  },

  ...clockMethods,
};

export default handler;
