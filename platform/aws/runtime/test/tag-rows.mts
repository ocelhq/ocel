import type { TagRecord } from "@framework/next-cache";
import type { TagRecordUpdate } from "@framework/next-runtime/use-cache-store";

export type TagRow = TagRecordUpdate & { tag: string };

export function publishedRecords(rows: Map<string, TagRow>): Record<string, TagRecord> {
  const records: Record<string, TagRecord> = {};
  for (const [tag, row] of rows) records[tag] = { stale: row.stale, expired: row.expired };
  return records;
}
