import { DynamoDBClient, QueryCommand, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { S3Client, GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { createHash } from "node:crypto";

import {
  isGuardRejection,
  tagRecordUpdate,
  tagSortKey,
  type TagRecordUpdate,
} from "./tag-index.mjs";

export type { TagRecordUpdate } from "./tag-index.mjs";

// A `use cache` entry exactly as it sits in object storage: the metadata and the
// body in one JSON document, so a read is a single GET and a write is atomic
// with no torn entry. The body is base64 for the same reason the ISR entry's is
// — it has to survive inside JSON.
export interface UseCacheEntry {
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
  body: string;
}

export interface TagRecordRow extends TagRecordUpdate {
  tag: string;
}

// `cursor` is DynamoDB's LastEvaluatedKey, opaque to the caller: present only
// when the page was truncated, and handed straight back to read the next one.
export interface TagRecordPage {
  records: TagRecordRow[];
  cursor?: unknown;
}

// UseCacheStore is the plural cache handlers' whole view of their backing
// services, so the cache semantics can be exercised without reaching AWS.
export interface UseCacheStore {
  readEntry(key: string): Promise<UseCacheEntry | null>;
  writeEntry(key: string, entry: UseCacheEntry): Promise<void>;
  queryTagRecords(since: number, cursor?: unknown): Promise<TagRecordPage>;
  writeTag(tag: string, record: TagRecordUpdate): Promise<boolean>;
}

// One page is deliberately large: a cold instance drains the whole partition,
// and every extra round trip is one more on its first request.
const tagPageSize = 200;

// The key Next hands a handler is an encodeReply blob of arbitrary bytes and
// arbitrary length. It is not a legal object key, so it is hashed into one.
function objectName(key: string): string {
  return createHash("sha256").update(key).digest("hex");
}

async function streamToString(body: any): Promise<string> {
  if (typeof body?.transformToString === "function") {
    return body.transformToString();
  }
  const chunks: Buffer[] = [];
  for await (const chunk of body) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks).toString("utf8");
}

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel use cache: ${name} is not set`);
  return value;
}

// awsUseCacheStore binds the store to the account-global state table. Tag keys
// are namespaced by the deploy, which is also what the function's IAM policy is
// scoped to — including on the index, whose partition key must therefore be the
// namespace exactly as granted or the query fails closed with a 403.
export function awsUseCacheStore(): UseCacheStore {
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");
  const index = env("OCEL_STATE_TABLE_INDEX");
  const bucket = env("OCEL_ISR_BUCKET");
  const prefix = env("OCEL_ISR_PREFIX");

  const ddb = new DynamoDBClient({});
  const s3 = new S3Client({});

  // Entries sit under the build's own prefix, which the function's existing
  // object grant already covers. Next seeds every `use cache` key with the build
  // id, so an app-scoped prefix would buy no extra sharing while widening the
  // grant — and build scoping means entries are cleaned up with the build.
  const objectKey = (key: string) => `${prefix}/use-cache/${objectName(key)}.json`;

  return {
    async readEntry(key) {
      try {
        const out = await s3.send(
          new GetObjectCommand({ Bucket: bucket, Key: objectKey(key) }),
        );
        return JSON.parse(await streamToString(out.Body));
      } catch (err: any) {
        if (err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404) {
          return null;
        }
        throw err;
      }
    },

    async writeEntry(key, entry) {
      await s3.send(
        new PutObjectCommand({
          Bucket: bucket,
          Key: objectKey(key),
          Body: JSON.stringify(entry),
          ContentType: "application/json",
        }),
      );
    },

    async queryTagRecords(since, cursor) {
      const out = await ddb.send(
        new QueryCommand({
          TableName: table,
          IndexName: index,
          // Inclusive of the cursor: a truncated page advances it only to the
          // last record consumed, and a strict `>` would then skip any record
          // sharing that millisecond. Re-reading the boundary record each sync
          // is the cheaper mistake, since merging a record is idempotent.
          KeyConditionExpression: "gsi1pk = :ns AND gsi1sk >= :since",
          ExpressionAttributeValues: {
            ":ns": { S: tagNamespace },
            ":since": { S: tagSortKey(since) },
          },
          Limit: tagPageSize,
          ExclusiveStartKey: cursor as Record<string, any> | undefined,
        }),
      );

      const records: TagRecordRow[] = [];
      for (const item of out.Items ?? []) {
        // The tag is an explicit attribute rather than a slice of the partition
        // key, so a reader never has to know the namespace it was written under.
        const tag = item.tag?.S;
        if (!tag) continue;
        records.push({
          tag,
          stale: item.stale?.N ? Number(item.stale.N) : undefined,
          expired: item.expired?.N ? Number(item.expired.N) : undefined,
          writtenAt: Number(item.gsi1sk?.S ?? 0),
        });
      }
      return { records, cursor: out.LastEvaluatedKey };
    },

    // Writes into the same record the incremental cache's tag store already
    // uses, under the same shared update, so both clocks observe every event.
    async writeTag(tag, record) {
      try {
        await ddb.send(
          new UpdateItemCommand(tagRecordUpdate(table, tagNamespace, tag, record)),
        );
        return true;
      } catch (err) {
        if (isGuardRejection(err)) return false;
        throw err;
      }
    },
  };
}
