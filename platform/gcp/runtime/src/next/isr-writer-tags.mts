import { mergeRecord, type TagRecord } from "@framework/next-cache";
import type { IsrWriterClient } from "@platform/edge-contract/isr-writer";
import type { TagRecords } from "./tag-records.mjs";

interface Waiter {
  resolve(): void;
  reject(error: unknown): void;
}

interface Batch {
  records: Map<string, TagRecord>;
  waiters: Waiter[];
}

export function newIsrWriterTagRecords(client: IsrWriterClient): TagRecords {
  let open: Batch | undefined;

  async function flush(batch: Batch): Promise<void> {
    open = undefined;
    try {
      await client.raiseTags(Object.fromEntries(batch.records));
    } catch (error) {
      for (const waiter of batch.waiters) waiter.reject(error);
      return;
    }
    for (const waiter of batch.waiters) waiter.resolve();
  }

  return {
    publish(tag, record) {
      return new Promise<void>((resolve, reject) => {
        if (open === undefined) {
          const batch: Batch = { records: new Map(), waiters: [] };
          open = batch;
          queueMicrotask(() => void flush(batch));
        }
        open.records.set(tag, mergeRecord(open.records.get(tag), record));
        open.waiters.push({ resolve, reject });
      });
    },

    async read(cursor) {
      try {
        const snapshot = await client.readTagSnapshot(cursor);
        switch (snapshot.kind) {
          case "fresh":
            return { status: "fresh", records: snapshot.snapshot.records, cursor: snapshot.etag };
          case "unchanged":
            return { status: "unchanged" };
          case "absent":
            return { status: "fresh", records: {}, cursor: null };
        }
      } catch {
        return { status: "unusable" };
      }
    },
  };
}
