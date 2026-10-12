import type { PurgeEntry } from "@platform/edge-contract/purge";

export type TagRecordWriter = (entries: readonly PurgeEntry[]) => Promise<void>;

let writer: TagRecordWriter | undefined;

export function setTagRecordWriter(write: TagRecordWriter | undefined): void {
  writer = write;
}

export function findTagRecordWriter(): TagRecordWriter | undefined {
  return writer;
}
