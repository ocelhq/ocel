import { tagClock } from "./tag-clock.mjs";

// Next's CacheHandler contract, restated here because the runtime types are not
// a dependency of this package (and the layer must bundle without one).
interface CacheEntry {
  value: ReadableStream<Uint8Array>;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

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

const MB = 1024 * 1024;

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

function resolveEntryCap(): number {
  const override = Number(process.env.OCEL_USE_CACHE_MAX_ENTRY);
  return override > 0 ? override : 5 * MB;
}

const maxBytes = resolveBudget();
const maxEntryBytes = resolveEntryCap();

const entries = new Map<string, StoredEntry>();
const pendingSets = new Map<string, Promise<void>>();
let usedBytes = 0;

function now(): number {
  return performance.timeOrigin + performance.now();
}

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

function streamOf(bytes: Uint8Array): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(bytes);
      controller.close();
    },
  });
}

function concat(chunks: Uint8Array[], size: number): Uint8Array {
  const out = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    out.set(chunk, at);
    at += chunk.byteLength;
  }
  return out;
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
      await pendingSets.get(cacheKey);

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
    let resolvePending = (): void => {};
    pendingSets.set(
      cacheKey,
      new Promise<void>((resolve) => {
        resolvePending = resolve;
      }),
    );

    try {
      const entry = await pendingEntry;
      // Tee'd so the caller's copy of the entry keeps an unconsumed stream.
      const [value, cloned] = entry.value.tee();
      entry.value = value;

      const reader = cloned.getReader();
      const chunks: Uint8Array[] = [];
      let size = 0;
      for (let chunk; !(chunk = await reader.read()).done; ) {
        size += chunk.value.byteLength;
        // Abandoned rather than finished: one oversized entry must not be
        // buffered whole just to be rejected, nor evict the working set.
        if (size > maxEntryBytes) {
          await reader.cancel().catch(() => {});
          return;
        }
        chunks.push(chunk.value);
      }

      store(cacheKey, {
        bytes: concat(chunks, size),
        tags: entry.tags,
        stale: entry.stale,
        timestamp: entry.timestamp,
        expire: entry.expire,
        revalidate: entry.revalidate,
      });
    } catch {
      // A stream that errored part-way leaves no entry: a truncated RSC payload
      // replayed to every later reader is worse than a miss.
    } finally {
      resolvePending();
      pendingSets.delete(cacheKey);
    }
  },

  async refreshTags(): Promise<void> {
    await tagClock.refreshTags();
  },

  async getExpiration(tags: string[]): Promise<number> {
    return tagClock.getExpiration(tags);
  },

  async updateTags(tags: string[], durations?: { expire?: number }): Promise<void> {
    await tagClock.updateTags(tags, durations);
  },
};

export default handler;
