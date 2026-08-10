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

const publishAttempts = 3;

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
