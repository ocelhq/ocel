import { createHash } from "node:crypto";
import { mergeRecord, type PublishTag, type TagRecord } from "@framework/next-cache";
import type { TagSnapshotRead } from "@framework/next-runtime/use-cache-store";
import { type Firestore, FirestoreError } from "./firestore.mjs";

export interface TagRecords {
  publish: PublishTag;
  read(cursor: string | null): Promise<TagSnapshotRead>;
}

export interface TagRecordsOptions {
  commitLagAllowanceMs?: number;
  rewriteAttempts?: number;
  contentionRounds?: number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
}

interface Waiter {
  resolve(): void;
  reject(error: Error): void;
}

interface PendingTag {
  record: TagRecord;
  contendedRounds: number;
  waiters: Waiter[];
}

const maxWritesPerCommit = 500;
const collectionId = "tags";
const contentionBackoffBaseMs = 250;
const contentionBackoffCeilingMs = 2000;

export function newFirestoreTagRecords(
  firestore: Firestore,
  isrPrefix: string,
  options: TagRecordsOptions = {},
): TagRecords {
  const prefix = `${isrPrefix}/`;
  const commitLagAllowanceMs = options.commitLagAllowanceMs ?? 2000;
  const rewriteAttempts = options.rewriteAttempts ?? 3;
  const contentionRounds = options.contentionRounds ?? 8;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const random = options.random ?? Math.random;

  let pending = new Map<string, PendingTag>();
  let flushing = false;

  function documentName(tag: string): string {
    const id = createHash("sha256")
      .update(prefix + tag)
      .digest("hex");
    return `${firestore.database}/documents/${collectionId}/${id}`;
  }

  function writeOf(tag: string, record: TagRecord) {
    const updateTransforms: unknown[] = [];
    if (record.stale !== undefined) {
      updateTransforms.push({
        fieldPath: "stale",
        maximum: { integerValue: String(record.stale) },
      });
    }
    if (record.expired !== undefined) {
      updateTransforms.push({
        fieldPath: "expired",
        maximum: { integerValue: String(record.expired) },
      });
    }
    updateTransforms.push({ fieldPath: "writtenAt", setToServerValue: "REQUEST_TIME" });
    return {
      update: {
        name: documentName(tag),
        fields: { prefix: { stringValue: prefix }, tag: { stringValue: tag } },
      },
      updateMask: { fieldPaths: ["prefix", "tag"] },
      updateTransforms,
    };
  }

  async function land(batch: [string, TagRecord][]): Promise<void> {
    let remaining = batch;
    for (let rewrites = 0; ; rewrites++) {
      const { commitTime, writeResults } = await firestore.commit(
        remaining.map(([tag, record]) => writeOf(tag, record)),
      );
      const committedAt = Date.parse(commitTime);
      const lagging = remaining.filter((_, i) => {
        const results = writeResults[i]?.transformResults ?? [];
        const stamped = (results[results.length - 1] as { timestampValue?: string } | undefined)
          ?.timestampValue;
        return stamped !== undefined && committedAt - Date.parse(stamped) > commitLagAllowanceMs;
      });
      if (lagging.length === 0) return;
      if (rewrites >= rewriteAttempts) {
        throw new Error(
          `ocel: tags ${lagging.map(([tag]) => tag).join(", ")} landed too long after they were stamped for other instances to read them`,
        );
      }
      remaining = lagging;
    }
  }

  function requeue(chunk: [string, PendingTag][]): void {
    for (const [tag, held] of chunk) {
      const waiting = pending.get(tag);
      if (waiting) {
        waiting.record = mergeRecord(waiting.record, held.record);
        waiting.waiters.push(...held.waiters);
        waiting.contendedRounds = Math.max(waiting.contendedRounds, held.contendedRounds);
      } else {
        pending.set(tag, held);
      }
    }
  }

  function retryAfterBackoff(chunk: [string, PendingTag][]): void {
    const rounds = Math.max(...chunk.map(([, held]) => held.contendedRounds));
    const backoffMs =
      random() * Math.min(contentionBackoffCeilingMs, contentionBackoffBaseMs * 2 ** (rounds - 1));
    void sleep(backoffMs).then(() => {
      requeue(chunk);
      void flush();
    });
  }

  async function flush(): Promise<void> {
    if (flushing) return;
    flushing = true;
    try {
      while (pending.size > 0) {
        const taken = pending;
        pending = new Map();
        const entries = [...taken.entries()];
        for (let from = 0; from < entries.length; from += maxWritesPerCommit) {
          const chunk = entries.slice(from, from + maxWritesPerCommit);
          try {
            await land(chunk.map(([tag, held]) => [tag, held.record]));
            for (const [, held] of chunk) for (const waiter of held.waiters) waiter.resolve();
          } catch (error) {
            const isContention = error instanceof FirestoreError && error.status === 409;
            if (
              isContention &&
              chunk.every(([, held]) => held.contendedRounds < contentionRounds)
            ) {
              for (const [, held] of chunk) held.contendedRounds++;
              retryAfterBackoff(chunk);
              continue;
            }
            const cause = error instanceof Error ? error.message : String(error);
            const failure = new Error(
              `ocel: could not record tags ${chunk.map(([tag]) => tag).join(", ")} in ${firestore.database}: ${cause}`,
            );
            for (const [, held] of chunk) for (const waiter of held.waiters) waiter.reject(failure);
          }
        }
      }
    } finally {
      flushing = false;
    }
  }

  function cursorFor(readTime: string): string {
    return new Date(Date.parse(readTime) - commitLagAllowanceMs).toISOString();
  }

  return {
    publish(tag, record) {
      return new Promise<void>((resolve, reject) => {
        const held = pending.get(tag);
        if (held) {
          held.record = mergeRecord(held.record, record);
          held.waiters.push({ resolve, reject });
        } else {
          pending.set(tag, {
            record: mergeRecord(undefined, record),
            contendedRounds: 0,
            waiters: [{ resolve, reject }],
          });
        }
        queueMicrotask(() => void flush());
      });
    },

    async read(cursor) {
      const onPrefix = {
        fieldFilter: {
          field: { fieldPath: "prefix" },
          op: "EQUAL",
          value: { stringValue: prefix },
        },
      };
      const where =
        cursor === null
          ? onPrefix
          : {
              compositeFilter: {
                op: "AND",
                filters: [
                  onPrefix,
                  {
                    fieldFilter: {
                      field: { fieldPath: "writtenAt" },
                      op: "GREATER_THAN",
                      value: { timestampValue: cursor },
                    },
                  },
                ],
              },
            };
      const { readTime, documents } = await firestore.runQuery({
        from: [{ collectionId }],
        where,
        orderBy: [{ field: { fieldPath: "writtenAt" }, direction: "ASCENDING" }],
      });
      const records: Record<string, TagRecord> = {};
      for (const { fields } of documents) {
        const tag = fields.tag?.stringValue as string | undefined;
        if (tag === undefined) continue;
        const record: TagRecord = {};
        if (fields.stale?.integerValue !== undefined)
          record.stale = Number(fields.stale.integerValue);
        if (fields.expired?.integerValue !== undefined) {
          record.expired = Number(fields.expired.integerValue);
        }
        records[tag] = record;
      }
      return { status: "fresh", records, etag: cursorFor(readTime) };
    },
  };
}
