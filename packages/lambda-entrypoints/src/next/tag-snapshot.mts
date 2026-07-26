import { mergeSnapshot, type TagRecord } from "@ocel/next-cache";

import { now } from "./use-cache-entry.mjs";
import type { UseCacheStore } from "./use-cache-store.mjs";

// The merge itself lives in @ocel/next-cache: the Cloudflare worker republishes
// the same replica, and a second writer merging by a different rule would make
// the document depend on who wrote it last. Only the publish loop — which needs
// this package's store — stays here.
export { latest, mergeRecord, mergeSnapshot } from "@ocel/next-cache";

// A publish loses only to another publisher landing first, and each retry starts
// from that publisher's snapshot. Convergence does not depend on winning: an
// exhausted publisher's records are carried by the next publish from any
// instance that has observed them, so the bound is small on purpose.
const publishAttempts = 3;

// Publishes the clock's merged map as this build's replica.
//
// Read, merge, conditional write, retry on precondition failure. Because the
// merge only moves watermarks upward, whichever writer wins a race produces a
// snapshot that contains both writers' invalidations, so no invalidation can be
// lost — and a publish that fails outright is repaired by the next one from any
// instance that has observed the same events through the index.
//
// Throws whatever the store throws, including on a stored snapshot that cannot
// be parsed: the caller's failure path is to leave the replica alone, which is
// the whole point of not replacing an unreadable one.
export async function publishTagSnapshot(
  store: UseCacheStore,
  records: Map<string, TagRecord>,
): Promise<boolean> {
  const snapshots = store.snapshots;
  if (!snapshots) return false;

  for (let attempt = 0; attempt < publishAttempts; attempt++) {
    const stored = await snapshots.read();
    const merged = mergeSnapshot(stored?.snapshot ?? null, records, now());
    if (await snapshots.write(merged, stored)) return true;
  }
  return false;
}
