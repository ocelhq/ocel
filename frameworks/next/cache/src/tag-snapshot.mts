import type { TagRecord, TagSnapshot } from "./index.mjs";

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

export function readableSnapshot(snapshot: unknown): TagSnapshot | null {
  if (typeof snapshot !== "object" || snapshot === null) return null;
  const { version, records } = snapshot as Partial<TagSnapshot>;
  if (version !== 1) return null;
  return records && typeof records === "object" ? (snapshot as TagSnapshot) : null;
}

export interface StoredTagSnapshot {
  snapshot: TagSnapshot;
  etag: string | null;
}

export interface TagSnapshotStore {
  read(): Promise<StoredTagSnapshot | null>;
  write(snapshot: TagSnapshot, prior: StoredTagSnapshot): Promise<boolean>;
}

const publishRounds = 5;

const backoffBaseMs = 25;

const backoffCeilingMs = 400;

async function publishOnce(
  store: TagSnapshotStore,
  records: Map<string, TagRecord>,
  at: number,
): Promise<boolean> {
  const stored = await store.read();
  if (stored === null) return true;
  if (stored.etag === null) {
    throw new Error(
      "ocel: the tag snapshot was read with no version, so a write could not be conditioned on it",
    );
  }
  return store.write(mergeSnapshot(stored.snapshot, records, at), stored);
}

function backoff(round: number): Promise<void> {
  const ceiling = Math.min(backoffCeilingMs, backoffBaseMs * 2 ** round);
  return new Promise((resolve) => setTimeout(resolve, Math.random() * ceiling));
}

export async function publishTagSnapshot(
  store: TagSnapshotStore,
  records: Map<string, TagRecord>,
  at: number,
): Promise<void> {
  for (let round = 0; round < publishRounds; round++) {
    if (await publishOnce(store, records, at)) return;
    if (round < publishRounds - 1) await backoff(round);
  }
  throw new Error(
    `ocel: could not publish ${[...records.keys()].join(", ")} to the tag snapshot: every attempt lost its version to another writer`,
  );
}

export type PublishTag = (tag: string, record: TagRecord) => Promise<void>;

export function newTagPublisher(store: TagSnapshotStore): PublishTag {
  let batch: { records: Map<string, TagRecord>; published: Promise<void> } | null = null;
  return (tag, record) => {
    if (batch === null) {
      const records = new Map<string, TagRecord>();
      const published = Promise.resolve().then(() => {
        batch = null;
        return publishTagSnapshot(store, records, Date.now());
      });
      batch = { records, published };
    }
    batch.records.set(tag, mergeRecord(batch.records.get(tag), record));
    return batch.published;
  };
}
