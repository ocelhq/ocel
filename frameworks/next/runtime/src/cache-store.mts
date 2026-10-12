import type { CacheEntryFile } from "@framework/next-cache";
import type { TagRecord } from "@platform/edge-contract/purge";

export interface CacheStore {
  readEntry(key: string): Promise<CacheEntryFile | null>;
  writeEntry(key: string, entry: CacheEntryFile): Promise<void>;
  readFetch(hash: string): Promise<CacheEntryFile | null>;
  writeFetch(hash: string, entry: CacheEntryFile): Promise<void>;
  writeTags(tags: string[], record: TagRecord): Promise<void>;
}
