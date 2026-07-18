import { tagClock, useCacheStore } from "./tag-clock.mjs";
import { bufferValue, now, streamOf, type CacheEntry } from "./use-cache-entry.mjs";

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
      // Fail closed. An empty tag map on an instance that has never synced means
      // "I have not learned about any invalidations", which must not be read as
      // "nothing was invalidated" — the entries here outlive this instance, so
      // there is real history to be ignorant of. A fresh render is correct,
      // merely slower.
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
      // refused the write leaves no entry: either way the cost is one re-render.
    }
  },

  async refreshTags(): Promise<void> {
    await tagClock.refreshTags();
  },

  async getExpiration(tags: string[]): Promise<number> {
    return tagClock.getExpiration(tags);
  },

  // Next fans updateTags out to every registered handler, so this and the
  // `default` handler both raise every invalidation. The shared clock collapses
  // them: the second durable write loses the monotonic guard and is swallowed.
  async updateTags(tags: string[], durations?: { expire?: number }): Promise<void> {
    await tagClock.updateTags(tags, durations);
  },
};

export default handler;
