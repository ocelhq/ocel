import { DynamoDBClient, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { S3Client, GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { createHash } from "node:crypto";

import {
  isGuardRejection,
  readableSnapshot,
  tagRecordUpdate,
  tagSnapshotKey,
  type TagRecord,
  type TagRecordUpdate,
  type TagSnapshot,
} from "@ocel/next-cache";

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

// The outcome of one conditional read of this build's tag clock. `etag` is the
// store's own version of the object, opaque to the clock and handed straight
// back on the next read.
//
// `unusable` is both an absent snapshot and one this reader cannot understand.
// Neither is an empty clock: a reader that took either for "nothing has been
// invalidated" would serve entries the fleet has already thrown away, so the two
// are one answer and the clock stays fail-closed on it.
export type TagSnapshotRead =
  | { status: "fresh"; records: Record<string, TagRecord>; etag: string | null }
  | { status: "unchanged" }
  | { status: "unusable" };

// UseCacheStore is the plural cache handlers' whole view of their backing
// services, so the cache semantics can be exercised without reaching AWS.
export interface UseCacheStore {
  readEntry(key: string): Promise<UseCacheEntry | null>;
  writeEntry(key: string, entry: UseCacheEntry): Promise<void>;
  readTagSnapshot(etag: string | null): Promise<TagSnapshotRead>;
  writeTag(tag: string, record: TagRecordUpdate): Promise<boolean>;
}

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

// S3 answers a satisfied If-None-Match with a bodiless 304, which the SDK
// surfaces as a thrown error like any other non-2xx.
function isNotModified(err: any): boolean {
  return err?.name === "NotModified" || err?.$metadata?.httpStatusCode === 304;
}

// awsUseCacheStore binds the store to the account-global state table for tag
// writes and to the build's own object prefix for everything it reads. Tag keys
// are namespaced by the deploy, which is also what the function's IAM policy is
// scoped to.
export function awsUseCacheStore(): UseCacheStore {
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");
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
        if (isNotFound(err)) return null;
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

    // The whole clock in one GET, conditioned on the version this instance
    // already merged: the publisher rewrites the object on every invalidation it
    // observes, so an unchanged object is proof nothing has been raised since.
    // What this replaces is a paged scan of the tag partition per cold instance.
    async readTagSnapshot(etag) {
      let out;
      try {
        out = await s3.send(
          new GetObjectCommand({
            Bucket: bucket,
            Key: tagSnapshotKey(prefix),
            ...(etag !== null ? { IfNoneMatch: etag } : {}),
          }),
        );
      } catch (err: any) {
        if (isNotModified(err)) return { status: "unchanged" };
        if (isNotFound(err)) return { status: "unusable" };
        throw err;
      }

      let snapshot: TagSnapshot | null = null;
      try {
        snapshot = readableSnapshot(JSON.parse(await streamToString(out.Body)));
      } catch {
        snapshot = null;
      }
      if (snapshot === null) return { status: "unusable" };

      return { status: "fresh", records: snapshot.records, etag: out.ETag ?? null };
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
