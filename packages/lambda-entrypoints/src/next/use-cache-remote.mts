import { clockMethods, tagClock, useCacheStore } from "./tag-clock.mjs";
import {
  bufferValue,
  now,
  pendingSets,
  streamOf,
  type CacheEntry,
} from "./use-cache-entry.mjs";

const pending = pendingSets();

// The `remote` cache kind, backing `use cache: remote`. Entries live in the
// account-global asset bucket under the build's own prefix, so every instance of
// a deployment shares one cache, a cold instance starts warm, and nothing is
// lost when the instance that wrote an entry goes away.
//
// The tier serves stale-while-revalidate, which it does by *not* filtering on
// revalidate: Next compares timestamp + revalidate itself once get() returns,
// serves the entry, and kicks a background regeneration that writes back through
// set(). The hard expire check below is what stops an entry riding that branch
// forever.
const handler = {
  // Next does not wrap get() in a try/catch, so a throw here surfaces as a
  // render error rather than a cache miss. Every failure becomes a miss.
  async get(cacheKey: string, _softTags: string[]): Promise<CacheEntry | undefined> {
    try {
      await pending.wait(cacheKey);

      // Entries here outlive the instance, so an unsynced clock has real history
      // to be ignorant of and must fail closed. The memory tier serves in the
      // same state, because its entries cannot predate its own map.
      if (!tagClock.hasSynced) return undefined;

      const store = useCacheStore();
      if (!store) return undefined;

      const stored = await store.readEntry(cacheKey);
      if (!stored) return undefined;

      if (now() > stored.timestamp + stored.expire * 1000) return undefined;
      if (tagClock.areTagsExpired(stored.tags, stored.timestamp)) return undefined;

      return {
        // Read from the stored bytes rather than handed through, so a second
        // read of the same entry is not defeated by a one-shot stream.
        value: streamOf(Buffer.from(stored.body, "base64")),
        tags: stored.tags,
        stale: stored.stale,
        timestamp: stored.timestamp,
        expire: stored.expire,
        // -1 is how Next's own handler signals tag-staleness: it forces the
        // serve-then-regenerate branch unconditionally.
        revalidate: tagClock.areTagsStale(stored.tags, stored.timestamp)
          ? -1
          : stored.revalidate,
      };
    } catch {
      return undefined;
    }
  },

  // set() runs while the response is already streaming, so a failure costs one
  // re-render and nothing else. It never throws.
  //
  // Buffer-then-put rather than a streamed upload: the entry is one JSON
  // envelope so a read is one round-trip and a write is atomic, and base64
  // needs every byte before it can produce any.
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
        // A stream that errored part-way leaves no entry, and a backend that
        // refused the write leaves no entry: either way the cost is one
        // re-render.
      }
    });
  },

  ...clockMethods,
};

export default handler;
