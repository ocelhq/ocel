import type {
  PointerRecordResult as StorePointerRecordResult,
  ServedRecordResult as StoreServedRecordResult,
} from "@platform/cf-releases-store/store";
import {
  parseStorageCoordinate,
  type StorageCoordinate,
} from "@platform/edge-contract/storage-key";
import { lruSet } from "./lru";
import type { ServedRecord } from "./served-record";

export interface StoredRecord extends ServedRecord {
  buildCoordinates?: Record<string, string>;
}

export function listOriginUrls(record: Pick<StoredRecord, "functionUrls" | "origin">): string[] {
  const urls = Object.values(record.functionUrls);
  return urls.length === 0 && record.origin ? [record.origin] : urls;
}

export type PointerRecordResult = StorePointerRecordResult<StoredRecord>;

export type ServedRecordResult = StoreServedRecordResult<StoredRecord>;

export interface ReleasesBinding {
  readServedRecord(args: { slug: string; storageCoordinate: string }): Promise<ServedRecordResult>;
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
  | { kind: "found"; record: StoredRecord; storage: StorageCoordinate }
  | { kind: "not-found" }
  | { kind: "unavailable" };

const RECORD_TTL_MS = 5_000;
export const RECORD_CACHE_MAX = 64;

interface CacheEntry {
  release: string;
  record: StoredRecord;
  storage: StorageCoordinate;
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
    return { kind: "found", record: cached.record, storage: cached.storage };
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
      return { kind: "found", record: cached.record, storage: cached.storage };
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
      return { kind: "found", record: cached.record, storage: cached.storage };
    case "record": {
      const storage = parseStorageCoordinate(result.record.storageCoordinate);
      if (storage === undefined) {
        console.error(
          `ocel: release ${result.release} of ${describeLookupScope(lookup)} locates none of its objects; answering 503`,
        );
        return { kind: "unavailable" };
      }
      lruSet(
        cache,
        key,
        { release: result.release, record: result.record, storage, at: now },
        RECORD_CACHE_MAX,
      );
      return { kind: "found", record: result.record, storage };
    }
  }
}
