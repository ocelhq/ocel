import { lruSet } from "./lru";
import type { ServedRecord } from "./served-record";

export interface StoredRecord extends ServedRecord {
  buildAssets?: Record<string, string>;
}

export type PointerRecordResult =
  | { kind: "no-pointer" }
  | { kind: "ambiguous-app" }
  | { kind: "unchanged"; release: string }
  | { kind: "record"; release: string; record: StoredRecord }
  | { kind: "dangling"; release: string };

export type ServedRecordResult =
  | { kind: "record"; record: StoredRecord }
  | { kind: "not-served" }
  | { kind: "forgotten" };

export interface ReleasesBinding {
  readServedRecord(args: { slug: string; isrPrefix: string }): Promise<ServedRecordResult>;
  readPointerRecord(args: {
    slug: string;
    app?: string;
    knownRelease?: string;
  }): Promise<PointerRecordResult>;
  readLabelRecord(args: {
    slug: string;
    label: string;
    knownRelease?: string;
  }): Promise<PointerRecordResult>;
  readGate(args: { slug: string }): Promise<string | undefined>;
}

export interface ReleaseLookup {
  binding: ReleasesBinding;
  slug: string;
  host: string;
  app?: string;
  label?: string;
  now?: () => number;
}

export type ReleaseResolution =
  | { kind: "found"; record: StoredRecord }
  | { kind: "not-found" }
  | { kind: "unavailable" };

const RECORD_TTL_MS = 5_000;
export const RECORD_CACHE_MAX = 64;

interface CacheEntry {
  release: string;
  record: StoredRecord;
  at: number;
}

const recordCache = new WeakMap<ReleasesBinding, Map<string, CacheEntry>>();

function cacheMap(binding: ReleasesBinding): Map<string, CacheEntry> {
  let map = recordCache.get(binding);
  if (!map) recordCache.set(binding, (map = new Map()));
  return map;
}

function cacheKey(lookup: ReleaseLookup): string {
  return lookup.host;
}

function describeLookupScope(lookup: ReleaseLookup): string {
  if (lookup.label !== undefined) return `${lookup.slug}/${lookup.label}`;
  if (lookup.app) return `${lookup.slug}/${lookup.app}`;
  return lookup.slug;
}

export async function resolveRelease(lookup: ReleaseLookup): Promise<ReleaseResolution> {
  const now = (lookup.now ?? Date.now)();
  const cache = cacheMap(lookup.binding);
  const key = cacheKey(lookup);
  const cached = cache.get(key);
  if (cached) lruSet(cache, key, cached, RECORD_CACHE_MAX);

  if (cached && now - cached.at < RECORD_TTL_MS) {
    return { kind: "found", record: cached.record };
  }

  let result: PointerRecordResult;
  try {
    const knownRelease = cached?.release;
    result =
      lookup.label === undefined
        ? await lookup.binding.readPointerRecord({
            slug: lookup.slug,
            app: lookup.app,
            knownRelease,
          })
        : await lookup.binding.readLabelRecord({
            slug: lookup.slug,
            label: lookup.label,
            knownRelease,
          });
  } catch (error) {
    const scope = describeLookupScope(lookup);
    if (cached) {
      const ageSeconds = Math.round((now - cached.at) / 1000);
      console.error(
        `ocel: the releases store did not answer for ${scope}; serving the record cached ${ageSeconds}s ago`,
        error,
      );
      return { kind: "found", record: cached.record };
    }
    console.error(`ocel: the releases store did not answer for ${scope}; answering 503`, error);
    return { kind: "unavailable" };
  }

  switch (result.kind) {
    case "no-pointer":
    case "ambiguous-app":
      return { kind: "not-found" };
    case "unchanged":
      if (!cached) return { kind: "unavailable" };
      lruSet(cache, key, { ...cached, at: now }, RECORD_CACHE_MAX);
      return { kind: "found", record: cached.record };
    case "record":
      lruSet(
        cache,
        key,
        { release: result.release, record: result.record, at: now },
        RECORD_CACHE_MAX,
      );
      return { kind: "found", record: result.record };
    case "dangling":
      return { kind: "unavailable" };
  }
}
