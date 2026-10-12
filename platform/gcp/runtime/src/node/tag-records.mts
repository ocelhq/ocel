import type { TagRecordWriter } from "@framework/node-runtime/tag-records";
import type { PurgeEntry, TagRecord } from "@platform/edge-contract/purge";
import type { PurgeTags } from "../cache/cdn-purge.mjs";

function buildTagRecord({ mode, raisedAt }: PurgeEntry): TagRecord {
  return mode === "purge" ? { expired: raisedAt } : { stale: raisedAt };
}

export function newTagRecordWriter(purge: PurgeTags | undefined): TagRecordWriter | undefined {
  if (!purge) return undefined;
  return (entries) => purge(new Map(entries.map((entry) => [entry.tag, buildTagRecord(entry)])));
}
