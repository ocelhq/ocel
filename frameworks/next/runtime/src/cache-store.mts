import type { CacheEntryFile, TagRecord } from "@framework/next-cache";

export interface CacheStore {
  readEntry(key: string): Promise<CacheEntryFile | null>;
  writeEntry(key: string, entry: CacheEntryFile): Promise<void>;
  readFetch(hash: string): Promise<CacheEntryFile | null>;
  writeFetch(hash: string, entry: CacheEntryFile): Promise<void>;
  writeTags(tags: string[], record: TagRecord): Promise<void>;
}
