import { DynamoDBClient, QueryCommand, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { S3Client, GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { createHash } from "node:crypto";

import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";

import { adoptedObjectStore } from "./object-store.mjs";
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

// A snapshot as it was found, with the etag the next write conditions on. The
// snapshot is null when the stored object could not be parsed: the etag is still
// returned, so a torn blob is overwritten under the same compare-and-swap rather
// than wedging the key forever.
export interface StoredTagSnapshot {
  snapshot: TagSnapshot | null;
  etag: string;
}

// TagSnapshotStore is the edge's replica of the tag clock, addressed as one
// object under compare-and-swap. Only ever written by the publisher and only
// ever read by it to merge — the authoritative clock is the state table.
export interface TagSnapshotStore {
  read(): Promise<StoredTagSnapshot | null>;
  // `etag` is the version this write replaces, or null to create the object
  // where none existed. False means the precondition failed: another publisher
  // got there first and the caller must re-read and merge onto their write.
  write(snapshot: TagSnapshot, etag: string | null): Promise<boolean>;
}

// UseCacheStore is the plural cache handlers' whole view of their backing
// services, so the cache semantics can be exercised without reaching AWS.
export interface UseCacheStore {
  readEntry(key: string): Promise<UseCacheEntry | null>;
  writeEntry(key: string, entry: UseCacheEntry): Promise<void>;
  queryTagRecords(since: number, cursor?: unknown): Promise<TagRecordPage>;
  writeTag(tag: string, record: TagRecordUpdate): Promise<boolean>;
  // null when this substrate adopted no object store: there is no edge reading a
  // replica, so the publisher stands down and the clock behaves exactly as it
  // did before replication existed.
  snapshots: TagSnapshotStore | null;
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

function isNotFound(err: any): boolean {
  return err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404;
}

// R2 returns 412 for a failed If-Match and 412 for a failed If-None-Match on
// PUT, which is the ordinary outcome of two publishers racing rather than an
// error. Nothing else may be swallowed here: a 403 means the token lost its
// grant, and silently reporting that as "someone else won" would leave the
// replica permanently and invisibly stale.
function isPreconditionFailure(err: any): boolean {
  return (
    err?.name === "PreconditionFailed" || err?.$metadata?.httpStatusCode === 412
  );
}

// The snapshot lives in the adopted store because that is the one the edge can
// read. An unadopted substrate has no edge replica at all, so there is nothing
// to publish and no bucket to publish it to.
function tagSnapshotStore(prefix: string): TagSnapshotStore | null {
  const store = adoptedObjectStore();
  if (!store) return null;

  const { client, bucket } = store;
  const key = tagSnapshotKey(prefix);

  return {
    async read() {
      try {
        const out = await client.send(
          new GetObjectCommand({ Bucket: bucket, Key: key }),
        );
        // Without an etag there is no version to condition the next write on,
        // so the object is treated as absent rather than written over blindly.
        if (!out.ETag) return null;
        const body = await streamToString(out.Body);
        try {
          return { snapshot: JSON.parse(body) as TagSnapshot, etag: out.ETag };
        } catch {
          return { snapshot: null, etag: out.ETag };
        }
      } catch (err: any) {
        if (isNotFound(err)) return null;
        throw err;
      }
    },

    async write(snapshot, etag) {
      try {
        await client.send(
          new PutObjectCommand({
            Bucket: bucket,
            Key: key,
            Body: JSON.stringify(snapshot),
            ContentType: "application/json",
            // Creating and replacing are both conditional, so a publisher that
            // read "absent" cannot clobber an object another publisher created
            // in the meantime — including the deploy's own seed, which is what
            // carries the build's pruning anchor.
            ...(etag === null ? { IfNoneMatch: "*" } : { IfMatch: etag }),
          }),
        );
        return true;
      } catch (err: any) {
        if (isPreconditionFailure(err)) return false;
        throw err;
      }
    },
  };
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
    snapshots: tagSnapshotStore(prefix),

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
