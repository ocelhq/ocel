import { clockMethods, tagClock } from "./tag-clock.mjs";
import {
  bufferValue,
  MB,
  now,
  pendingSets,
  streamOf,
  type CacheEntry,
} from "./use-cache-entry.mjs";

interface StoredEntry {
  bytes: Uint8Array;
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
}

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

const handler = {
  async get(cacheKey: string, _softTags: string[]): Promise<CacheEntry | undefined> {
    try {
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
        const entry = await pendingEntry;
        const bytes = await bufferValue(entry);
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
      }
    });
  },

  ...clockMethods,
};

export default handler;
