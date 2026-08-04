import { publishTagSnapshot, type TagRecord } from "@ocel/next-cache";

import { S3TagSnapshotStore, type S3Commands, type S3Like } from "./snapshot.mjs";
import { raise } from "./writer.mjs";
import type { Raises } from "./records.mjs";

export interface Publisher {
  s3: S3Like;
  commands: S3Commands;
  fetch: typeof fetch;
  assetBucket: string;
  endpoint: string;
  seed: string;
}

// publish writes one build's copies of the clock: the S3 one directly, then the
// edge's through that build's snapshot Durable Object.
//
// S3 first, deliberately. Both writes carry the same records and the merge is
// monotone, so the order cannot change what either copy converges to — but the
// S3 write is in-region and the raise crosses the internet, so doing the cheap
// one first means a batch that fails on the raise has already advanced the copy
// ocelhq-wvag.5's origin reads.
async function publishOne(
  publisher: Publisher,
  isrPrefix: string,
  records: Map<string, TagRecord>,
  at: number,
): Promise<void> {
  const store = new S3TagSnapshotStore(
    publisher.s3,
    publisher.commands,
    publisher.assetBucket,
    isrPrefix,
  );
  if (!(await publishTagSnapshot(store, records, at))) {
    throw new Error(`publish ${isrPrefix}: every attempt lost the object's version`);
  }
  await raise(publisher.fetch, publisher.endpoint, publisher.seed, isrPrefix, records);
}

// publishAll fans out over the builds a batch touched. One build failing fails
// the batch, which is what sends it back for a retry and eventually to the
// dead-letter queue — but every build is attempted first, so one wedged build
// does not hold up the rest of a batch that would otherwise have landed.
//
// Republishing what already landed is free: the raise is idempotent by
// construction, and an S3 merge that changes nothing writes the same document.
export async function publishAll(
  publisher: Publisher,
  raises: Raises,
  at: number,
): Promise<void> {
  const results = await Promise.allSettled(
    [...raises].map(([isrPrefix, records]) =>
      publishOne(publisher, isrPrefix, records, at),
    ),
  );
  const failures = results.filter((r) => r.status === "rejected");
  if (failures.length > 0) {
    throw new AggregateError(
      failures.map((f) => f.reason),
      `${failures.length} of ${results.length} builds in this batch were not published`,
    );
  }
}
