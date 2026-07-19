import { type TagRecord, type TagSnapshot } from "@ocel/next-cache";

import { now } from "./use-cache-entry.mjs";
import type { UseCacheStore } from "./use-cache-store.mjs";

// How long a published snapshot may be trusted by the edge. The window is the
// only bound on how long a failed publish can leave the replica stale, because
// every other repair path needs a Lambda and a fully intercepted workload wakes
// none. Past it the edge falls open, which wakes a Lambda, which republishes —
// so the window also sets how often a completely idle build pays one origin
// render.
//
// Five minutes buys both cheaply: a worst-case staleness a user can be told
// about plainly, at a cost of roughly twelve origin renders an hour per build,
// far below what ISR revalidation already spends.
export const snapshotValidityMs = 5 * 60_000;

// Republished at half the window so the trust window is renewed well before it
// lapses, rather than after the edge has already started falling open.
export const snapshotRefreshMs = snapshotValidityMs / 2;

// A publish loses only to another publisher landing first, and each retry starts
// from that publisher's snapshot. Convergence does not depend on winning: an
// exhausted publisher's records are carried by the next publish from any
// instance that has observed them, so the bound is small on purpose.
const publishAttempts = 3;

// Always upwards: a record arriving from anywhere must never walk back an
// invalidation the reader already knows about, which is what makes merging
// order-independent and the publish convergent.
export function latest(a: number | undefined, b: number | undefined): number | undefined {
  if (a === undefined) return b;
  if (b === undefined) return a;
  return Math.max(a, b);
}

export function mergeRecord(existing: TagRecord | undefined, incoming: TagRecord): TagRecord {
  return {
    stale: latest(existing?.stale, incoming.stale),
    expired: latest(existing?.expired, incoming.expired),
  };
}

// A record can only ever expire or stale an entry whose lastModified precedes
// its watermark, and every entry under a build's prefix was written at or after
// that build deployed. So a record with both watermarks at or before the deploy
// time is inert for this build and can be dropped — which is what keeps the
// snapshot bounded on a substrate that has been invalidating tags for months.
//
// An unanchored snapshot has deployedAt 0, and no real watermark sits at or
// before zero, so nothing is pruned. That is the honest outcome: without the
// deploy's own timestamp there is no proof, and over-pruning would silently
// resurrect stale content at the edge.
function isInert(record: TagRecord, deployedAt: number): boolean {
  return (record.stale ?? 0) <= deployedAt && (record.expired ?? 0) <= deployedAt;
}

export function mergeSnapshot(
  prior: TagSnapshot | null,
  records: Map<string, TagRecord>,
  at: number,
): TagSnapshot {
  const deployedAt = prior?.deployedAt ?? 0;
  const merged: Record<string, TagRecord> = {};

  const priorRecords = prior?.records ?? {};
  for (const tag of new Set([...Object.keys(priorRecords), ...records.keys()])) {
    const record = mergeRecord(priorRecords[tag], records.get(tag) ?? {});
    if (!isInert(record, deployedAt)) merged[tag] = record;
  }

  return {
    version: 1,
    deployedAt,
    generatedAt: at,
    validUntil: at + snapshotValidityMs,
    records: merged,
  };
}

// Publishes the clock's merged map as this build's replica.
//
// Read, merge, conditional write, retry on precondition failure. Because the
// merge only moves watermarks upward, whichever writer wins a race produces a
// snapshot that contains both writers' invalidations, so no invalidation can be
// lost — and a publish that fails outright is repaired by the next one from any
// instance that has observed the same events through the index.
export async function publishTagSnapshot(
  store: UseCacheStore,
  records: Map<string, TagRecord>,
): Promise<boolean> {
  const snapshots = store.snapshots;
  if (!snapshots) return false;

  for (let attempt = 0; attempt < publishAttempts; attempt++) {
    const stored = await snapshots.read();
    const merged = mergeSnapshot(stored?.snapshot ?? null, records, now());
    if (await snapshots.write(merged, stored?.etag ?? null)) return true;
  }
  return false;
}
