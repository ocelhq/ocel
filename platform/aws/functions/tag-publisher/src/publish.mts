import { publishTagSnapshot, type TagRecord } from "@framework/next-cache";

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
