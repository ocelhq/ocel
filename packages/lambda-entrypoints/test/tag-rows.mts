import type { TagRecord, TagRecordUpdate } from "@ocel/next-cache";

// The state table's tag partition as a fake store holds it, and the snapshot a
// publisher would produce from those rows — the same records with the write
// watermark, which is the publisher's business alone, left behind.
export type TagRow = TagRecordUpdate & { tag: string };

export function publishedRecords(rows: Map<string, TagRow>): Record<string, TagRecord> {
  const records: Record<string, TagRecord> = {};
  for (const [tag, row] of rows) records[tag] = { stale: row.stale, expired: row.expired };
  return records;
}
