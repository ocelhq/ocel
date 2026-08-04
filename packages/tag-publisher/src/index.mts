import { GetObjectCommand, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { SSMClient } from "@aws-sdk/client-ssm";

import { config } from "./config.mjs";
import { publishAll } from "./publish.mjs";
import { raisesOf, type StreamRecord } from "./records.mjs";

// The account-global tag-snapshot publisher.
//
// A tag invalidation is durable the moment it lands in the state table, and
// that write IS the raise. This function is what carries it the rest of the
// way: it reads the record off the table's stream, writes the S3 copy of that
// build's tag clock directly, and posts the raise to the build's snapshot
// Durable Object, which owns the R2 copy the edge reads. One publisher for the
// whole substrate, and nothing left contending on a key R2 rate-limits at one
// write a second.
//
// It is at-least-once and idempotent by construction — the merge only ever
// moves a watermark upward — which is what makes an event source mapping's two
// sanctioned readers per shard harmless.
//
// It also does not see every write. tagRecordUpdate's condition expression
// means a duplicate raise that loses to a larger watermark fails the condition
// and emits no stream record at all. That is correct for a publisher, whose job
// is the current value rather than the history — but it does mean the record
// count here is not the invalidation count.

const s3 = new S3Client({});
const ssm = new SSMClient({});

// The response is ReportBatchItemFailures (declared on the event source mapping
// in cloud/aws/bootstrap/publisher.go): only the records of builds that failed
// are retried, so one build that can never publish does not carry its
// batch-mates to the dead-letter queue with it.
export interface BatchResponse {
  batchItemFailures: { itemIdentifier: string }[];
}

export const handler = async (event: { Records?: StreamRecord[] }): Promise<BatchResponse> => {
  const raises = raisesOf(event.Records ?? []);
  if (raises.size === 0) return { batchItemFailures: [] };

  const { assetBucket, endpoint, seed } = await config(ssm);
  const failed = await publishAll(
    { s3, commands: { GetObjectCommand, PutObjectCommand }, fetch, assetBucket, endpoint, seed },
    raises,
    Date.now(),
  );
  return { batchItemFailures: failed.map((itemIdentifier) => ({ itemIdentifier })) };
};
