import type { TagRecord } from "@framework/next-cache";

export interface UseCacheEntry {
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
  body: string;
}

export interface TagRecordUpdate extends TagRecord {
  writtenAt: number;
}

export type TagSnapshotRead =
  | { status: "fresh"; records: Record<string, TagRecord>; cursor: string | null }
  | { status: "unchanged" }
  | { status: "unusable" };

export interface UseCacheStore {
  readEntry(key: string): Promise<UseCacheEntry | null>;
  writeEntry(key: string, entry: UseCacheEntry): Promise<void>;
  readTagSnapshot(cursor: string | null): Promise<TagSnapshotRead>;
  writeTag(tag: string, record: TagRecordUpdate): Promise<boolean>;
}
