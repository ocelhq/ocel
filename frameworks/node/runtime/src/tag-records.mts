import type { PurgeEntry } from "@platform/edge-contract/purge";

export type WriteTagRecords = (entries: readonly PurgeEntry[]) => Promise<void>;

let writer: WriteTagRecords | undefined;

export function setTagRecordWriter(write: WriteTagRecords | undefined): void {
  writer = write;
}

export function findTagRecordWriter(): WriteTagRecords | undefined {
  return writer;
}
