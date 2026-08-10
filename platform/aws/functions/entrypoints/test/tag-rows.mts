import type { TagRecord, TagRecordUpdate } from "@framework/next-cache";

export type TagRow = TagRecordUpdate & { tag: string };

export function publishedRecords(rows: Map<string, TagRow>): Record<string, TagRecord> {
  const records: Record<string, TagRecord> = {};
  for (const [tag, row] of rows) records[tag] = { stale: row.stale, expired: row.expired };
  return records;
}
