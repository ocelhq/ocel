import type { WriteTagRecords } from "@framework/node-runtime/tag-records";
import type { PurgeEntry, TagRecord } from "@platform/edge-contract/purge";
import type { PurgeTags } from "../cache/cdn-purge.mjs";

function recordOf({ mode, raisedAt }: PurgeEntry): TagRecord {
  return mode === "purge" ? { expired: raisedAt } : { stale: raisedAt };
}

export function newGcpTagRecordWriter(purge: PurgeTags | undefined): WriteTagRecords | undefined {
  if (!purge) return undefined;
  return (entries) => purge(new Map(entries.map((entry) => [entry.tag, recordOf(entry)])));
}
