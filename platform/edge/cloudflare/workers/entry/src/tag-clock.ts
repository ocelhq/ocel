import {
  readableSnapshot,
  type TagFreshness,
  type TagRecord,
  type TagSnapshot,
  tagFreshness,
  tagSnapshotKey,
} from "@framework/next-cache";

import { settledWithin } from "./cache";

export interface StoredObject {
  etag?: string;
  text(): Promise<string>;
}

export interface ObjectStoreReader {
  get(key: string): Promise<StoredObject | null>;
}

export interface TagClockDeps {
  store: ObjectStoreReader;
  snapshotReadTimeoutMs?: number;
}

export const defaultSnapshotReadTimeoutMs = 3_000;

export type TagVerdict = TagFreshness | "untrusted";

export interface TagClock {
  freshness(tags: string[], timestamp: number, now: number): Promise<TagVerdict>;
}

const snapshotMemoMs = 1_000;

interface SnapshotCell {
  memo?: { at: number; snapshot: TagSnapshot | null };
  read?: Promise<TagSnapshot | null>;
}

const snapshotCells = new WeakMap<ObjectStoreReader, Map<string, SnapshotCell>>();

function snapshotCell(cfg: { isrPrefix: string }, store: ObjectStoreReader): SnapshotCell {
  let byPrefix = snapshotCells.get(store);
  if (!byPrefix) snapshotCells.set(store, (byPrefix = new Map()));
  let cell = byPrefix.get(cfg.isrPrefix);
  if (!cell) byPrefix.set(cfg.isrPrefix, (cell = {}));
  return cell;
}

export function dropSnapshotMemo(cfg: { isrPrefix: string }, store: ObjectStoreReader): void {
  snapshotCells.get(store)?.delete(cfg.isrPrefix);
}

export function createTagClock(cfg: { isrPrefix: string }, deps: TagClockDeps): TagClock {
  return {
    async freshness(tags, timestamp, now) {
      if (tags.length === 0) return "fresh";
      try {
        const snapshot = await readSnapshot(cfg, deps, now);
        if (!snapshot) return "untrusted";
        const records: Map<string, TagRecord> = new Map(Object.entries(snapshot.records));
        return tagFreshness(tags, records, timestamp, now);
      } catch {
        return "untrusted";
      }
    },
  };
}

async function readSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<TagSnapshot | null> {
  const cell = snapshotCell(cfg, deps.store);
  if (cell.memo && now - cell.memo.at < snapshotMemoMs) return cell.memo.snapshot;

  if (!cell.read) {
    const read: Promise<TagSnapshot | null> = fillSnapshot(cfg, deps, now, cell);
    cell.read = read;
    read
      .finally(() => {
        if (cell.read === read) cell.read = undefined;
      })
      .catch(() => {});
  }

  const pending = cell.read;
  const timeoutMs = deps.snapshotReadTimeoutMs ?? defaultSnapshotReadTimeoutMs;
  if (await settledWithin(pending, timeoutMs)) return pending;

  if (cell.read === pending) cell.read = undefined;
  return null;
}

async function fillSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
  cell: SnapshotCell,
): Promise<TagSnapshot | null> {
  const object = await deps.store.get(tagSnapshotKey(cfg.isrPrefix));
  const body = object ? await object.text() : null;
  const snapshot = body === null ? null : readableSnapshot(parseJson<TagSnapshot>(body));
  cell.memo = { at: now, snapshot };
  return snapshot;
}

export function parseJson<T>(body: string): T | null {
  try {
    return JSON.parse(body) as T;
  } catch {
    return null;
  }
}
