import type { TagRecord, TagSnapshot } from "./index.mjs";

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

  return { version: 1, deployedAt, generatedAt: at, records: merged };
}
