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

// A replica is read only at a version this reader was written against. An
// unknown version is a format it cannot claim to understand, so it declines to
// guess — which is what lets the format change without a fleet misreading it.
// This is the whole of what a reader may judge: everything else about the
// document is the publisher's to assert.
//
// Every publisher runs it too, and for a stronger reason than a reader does:
// merging into a document it cannot read would write away whatever that format
// carries, the deploy anchor included.
// The argument is whatever the object store handed back, so it is typed as what
// it is: parsed bytes no one has vouched for yet.
export function readableSnapshot(snapshot: unknown): TagSnapshot | null {
  if (typeof snapshot !== "object" || snapshot === null) return null;
  const { version, records } = snapshot as Partial<TagSnapshot>;
  if (version !== 1) return null;
  return records && typeof records === "object" ? (snapshot as TagSnapshot) : null;
}

// A snapshot as it was found, with the version the next write conditions on. A
// null etag means the object exists but the store named no version for it, which
// is a write that has to proceed unconditionally rather than one that can never
// satisfy its precondition.
export interface StoredTagSnapshot {
  snapshot: TagSnapshot;
  etag: string | null;
}

// TagSnapshotStore is the edge's replica of the tag clock, addressed as one
// object under compare-and-swap. Only ever written by a publisher and only ever
// read by one to merge — the authoritative clock is the state table.
//
// This is the whole of what differs between the two publishers: the Lambda
// reaches the replica over the S3-compatible API and spells the precondition
// If-Match/If-None-Match, the worker over a native R2 binding and spells it
// etagMatches/etagDoesNotMatch. The loop below is the same on both.
export interface TagSnapshotStore {
  // Throws rather than reporting an unparseable snapshot as absent. Replacing
  // one would publish a snapshot with no deploy anchor, and since the anchor is
  // written only by the deploy's genesis seed, that disables pruning for the
  // life of the build. Declining costs one build until its next deploy re-seeds
  // — and the edge already falls open on a snapshot it cannot parse — where
  // clobbering the anchor is unbounded.
  read(): Promise<StoredTagSnapshot | null>;
  // `prior` is what the write is conditioned on, and is always a document this
  // publisher has read: nothing here ever creates the object. False means the
  // precondition failed — another publisher got there first and the caller must
  // re-read and merge onto their write.
  write(snapshot: TagSnapshot, prior: StoredTagSnapshot): Promise<boolean>;
}

// A publish loses only to another publisher landing first, and each retry starts
// from that publisher's snapshot. Convergence does not depend on winning: an
// exhausted publisher's records are carried by the next publish from any
// instance that has observed them, so the bound is small on purpose.
const publishAttempts = 3;

// Publishes `records` into this build's replica: read, merge, conditional
// write, retry on precondition failure. False when every attempt lost.
//
// A build with no replica gets none created for it. deployedAt has exactly one
// writer — the deploy's genesis seed — and a document conjured here would carry
// a zero anchor, against which no record is ever inert, so that build's replica
// would accumulate every tag it invalidates for the life of the build. Nothing
// to publish into is not a failure: the reader of an absent replica falls open
// to the authoritative clock, and only a deploy can put one there.
//
// Because the merge only moves watermarks upward, whichever writer wins a race
// produces a snapshot that contains both writers' invalidations, so no
// invalidation can be lost — and a publish that fails outright is repaired by
// the next one from any instance that has observed the same events.
//
// Throws whatever the store throws, including on a stored snapshot that cannot
// be parsed: the caller's failure path is to leave the replica alone, which is
// the whole point of not replacing an unreadable one.
export async function publishTagSnapshot(
  store: TagSnapshotStore,
  records: Map<string, TagRecord>,
  at: number,
): Promise<boolean> {
  for (let attempt = 0; attempt < publishAttempts; attempt++) {
    const stored = await store.read();
    if (stored === null) return true;
    if (await store.write(mergeSnapshot(stored.snapshot, records, at), stored)) return true;
  }
  return false;
}
