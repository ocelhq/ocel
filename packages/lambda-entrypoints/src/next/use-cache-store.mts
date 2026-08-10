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

export interface UseCacheEntry {
  tags: string[];
  stale: number;
  timestamp: number;
  expire: number;
  revalidate: number;
  body: string;
}

export type TagSnapshotRead =
  | { status: "fresh"; records: Record<string, TagRecord>; etag: string | null }
  | { status: "unchanged" }
  | { status: "unusable" };

export interface UseCacheStore {
  readEntry(key: string): Promise<UseCacheEntry | null>;
  writeEntry(key: string, entry: UseCacheEntry): Promise<void>;
  readTagSnapshot(etag: string | null): Promise<TagSnapshotRead>;
  writeTag(tag: string, record: TagRecordUpdate): Promise<boolean>;
}

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

function isNotModified(err: any): boolean {
  return err?.name === "NotModified" || err?.$metadata?.httpStatusCode === 304;
}

export function awsUseCacheStore(): UseCacheStore {
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");
  const bucket = env("OCEL_ISR_BUCKET");
  const prefix = env("OCEL_ISR_PREFIX");

  const ddb = new DynamoDBClient({});
  const s3 = new S3Client({});

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
