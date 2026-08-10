export interface FetchCacheEntry {
  lastModified: number;
  value: Record<string, unknown>;
}

export interface EdgeCacheRpc {
  fetchGet(scope: string, key: string, tags: string[]): Promise<FetchCacheEntry | null>;
  fetchSet(scope: string, key: string, entry: FetchCacheEntry, tags: string[]): Promise<void>;
  revalidateTags(scope: string, tags: string[], durations?: { expire?: number }): Promise<void>;
}
