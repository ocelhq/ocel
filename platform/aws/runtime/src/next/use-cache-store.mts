import { createHash } from "node:crypto";
import { DynamoDBClient, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { GetObjectCommand, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";

import {
  type PublishTag,
  readableSnapshot,
  type TagSnapshot,
  tagSnapshotKey,
} from "@framework/next-cache";
import type { UseCacheStore } from "@framework/next-runtime/use-cache-store";
import { isNotFound, readBodyText, requireEnv } from "./object-store.mjs";
import { isGuardRejection, tagRecordUpdate } from "./tag-index.mjs";

function objectName(key: string): string {
  return createHash("sha256").update(key).digest("hex");
}

function isNotModified(err: any): boolean {
  return err?.name === "NotModified" || err?.$metadata?.httpStatusCode === 304;
}

export function newAwsUseCacheStore(publish: PublishTag): UseCacheStore {
  const table = requireEnv("OCEL_STATE_TABLE");
  const tagNamespace = requireEnv("OCEL_ISR_TAG_NAMESPACE");
  const bucket = requireEnv("OCEL_ISR_BUCKET");
  const prefix = requireEnv("OCEL_ISR_PREFIX");

  const ddb = new DynamoDBClient({});
  const s3 = new S3Client({});

  const objectKey = (key: string) => `${prefix}/use-cache/${objectName(key)}.json`;

  return {
    async readEntry(key) {
      try {
        const out = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: objectKey(key) }));
        return JSON.parse(await readBodyText(out.Body));
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
        snapshot = readableSnapshot(JSON.parse(await readBodyText(out.Body)));
      } catch {
        snapshot = null;
      }
      if (snapshot === null) return { status: "unusable" };

      return { status: "fresh", records: snapshot.records, etag: out.ETag ?? null };
    },

    async writeTag(tag, record) {
      const indexed = async () => {
        try {
          await ddb.send(new UpdateItemCommand(tagRecordUpdate(table, tagNamespace, tag, record)));
          return true;
        } catch (err) {
          if (isGuardRejection(err)) return false;
          throw err;
        }
      };
      const { writtenAt: _, ...published } = record;
      const [written] = await Promise.all([indexed(), publish(tag, published)]);
      return written;
    },
  };
}
