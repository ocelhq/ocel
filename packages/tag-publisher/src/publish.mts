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

// publishAll fans out over the builds a batch touched and answers with the
// stream records that must be retried: those of the builds that failed, and no
// others.
//
// Reporting per record rather than throwing is what keeps one build's failure
// its own. A build whose deploy never initialized the writer 401s forever, and
// as a whole-batch failure it drags every healthy build sharing that batch
// through five retries and into the dead-letter queue with it.
//
// Republishing what already landed is free: the raise is idempotent by
// construction, and an S3 merge that changes nothing writes the same document.
export async function publishAll(
  publisher: Publisher,
  raises: Raises,
  at: number,
): Promise<string[]> {
  const builds = [...raises];
  const results = await Promise.allSettled(
    builds.map(([isrPrefix, raise]) => publishOne(publisher, isrPrefix, raise.records, at)),
  );
  return builds.flatMap(([isrPrefix, raise], i) => {
    const result = results[i]!;
    if (result.status === "fulfilled") return [];
    console.error(`ocel: publishing ${isrPrefix} failed`, result.reason);
    return raise.sequenceNumbers;
  });
}
