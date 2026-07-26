// A stored data-cache entry, as both sides exchange it over RPC. Same shape as
// a route entry's CacheEntryFile, but named apart because the two live in
// different buckets: fetch entries hold upstream response bodies, which are
// origin-private and never reach an adopted edge store.
export interface FetchCacheEntry {
  lastModified: number;
  value: Record<string, unknown>;
}

// What the edge-bundled cache handler may ask of the main worker. Deliberately
// narrow: the edge holds no credentials and no store bindings, so every call
// names the deployment scope it applies to and the worker does the rest.
//
// `scope` is the deployment's ISR prefix (`prod/proj/app/build`). The worker
// needs nothing else to address a deployment: the entries sit under that prefix,
// and the DynamoDB tag namespace is derived from the same string (tagNamespace).
export interface EdgeCacheRpc {
  // null on a miss, and also when the entry's tags have been invalidated — the
  // worker owns tag evaluation because it owns the clock.
  //
  // Null rather than a staleness flag because Next already computes fetch-entry
  // staleness itself, from the entry's own `revalidate` against `lastModified`;
  // a handler that returned early-stale signals would be duplicating logic Next
  // owns and can change under it. Tag expiry is the one case that genuinely has
  // to be a hard miss: tag-invalidated content must never be served, not even
  // once while a revalidation runs.
  fetchGet(scope: string, key: string, tags: string[]): Promise<FetchCacheEntry | null>;
  fetchSet(scope: string, key: string, entry: FetchCacheEntry, tags: string[]): Promise<void>;
  // Mirrors the clock's own arithmetic: the tags go stale now and, only if an
  // expire window is given, dead at the end of it.
  revalidateTags(scope: string, tags: string[], durations?: { expire?: number }): Promise<void>;
}
