import { clockMethods, tagClock } from "./tag-clock.mjs";
import {
  bufferValue,
  MB,
  now,
  pendingSets,
  streamOf,
  type CacheEntry,
} from "./use-cache-entry.mjs";

// An entry as it is held: the stream buffered to bytes, because the stream Next
// hands over is one-shot and every read has to get its own.
interface StoredEntry {
  bytes: Uint8Array;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

// The whole cache lives in the function's own heap, so the budget has to scale
// with the memory the function was actually given — a fifth of a 256MB function
// is not the same bet as a fifth of a 3GB one. AWS always sets the memory size
// in the environment; the fixed fallback is for running outside Lambda.
function resolveBudget(): number {
  const override = Number(process.env.OCEL_USE_CACHE_MAX_BYTES);
  if (override > 0) return override;
  const memoryMb = Number(process.env.AWS_LAMBDA_FUNCTION_MEMORY_SIZE);
  if (memoryMb > 0) return Math.floor(memoryMb * MB * 0.1);
  return 50 * MB;
}

const maxBytes = resolveBudget();

const entries = new Map<string, StoredEntry>();
const pending = pendingSets();
let usedBytes = 0;

// Map iteration order is insertion order, so re-inserting on every touch makes
// the first key the least recently used one.
function touch(key: string, stored: StoredEntry): void {
  entries.delete(key);
  entries.set(key, stored);
}

function store(key: string, stored: StoredEntry): void {
  const existing = entries.get(key);
  if (existing) usedBytes -= existing.bytes.byteLength;
  touch(key, stored);
  usedBytes += stored.bytes.byteLength;

  while (usedBytes > maxBytes) {
    const oldest = entries.keys().next().value;
    if (oldest === undefined) break;
    usedBytes -= entries.get(oldest)!.bytes.byteLength;
    entries.delete(oldest);
  }
}

// The `default` cache kind, backing `use cache`. A byte-bounded LRU in the
// instance's own memory: fast, process-local, and gone when the instance is.
//
// Time-staleness is a miss rather than stale-while-revalidate, following Next's
// own reasoning for this tier — warming an entry that will likely be evicted
// before anyone reuses it is not worth the request that pays for it.
const handler = {
  // Next does not wrap get() in a try/catch, so a throw here surfaces as a
  // render error rather than a cache miss. Every failure becomes a miss.
  async get(cacheKey: string, _softTags: string[]): Promise<CacheEntry | undefined> {
    try {
      // A read that arrives mid-fill waits for it, rather than wasting the fill
      // by reporting a miss and re-rendering alongside it.
      await pending.wait(cacheKey);

      const stored = entries.get(cacheKey);
      if (!stored) return undefined;
      if (now() > stored.timestamp + stored.revalidate * 1000) return undefined;
      if (tagClock.areTagsExpired(stored.tags, stored.timestamp)) return undefined;

      touch(cacheKey, stored);

      return {
        value: streamOf(stored.bytes),
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
  async set(cacheKey: string, pendingEntry: Promise<CacheEntry>): Promise<void> {
    await pending.run(cacheKey, async () => {
      try {
        const entry = await pendingEntry;
        const bytes = await bufferValue(entry);
        // An oversized entry evicts nothing: one giant page must not cost the
        // whole working set.
        if (!bytes) return;

        store(cacheKey, {
          bytes,
          tags: entry.tags,
          stale: entry.stale,
          timestamp: entry.timestamp,
          expire: entry.expire,
          revalidate: entry.revalidate,
        });
      } catch {
        // A stream that errored part-way leaves no entry: a truncated RSC
        // payload replayed to every later reader is worse than a miss.
      }
    });
  },

  ...clockMethods,
};

export default handler;
