import { DynamoDBClient, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";

import { isrEntryStore, type EntryStore } from "./isr-writer.mjs";
import {
  entriesAdopted,
  providerObjectStore,
  type ObjectStore,
} from "./object-store.mjs";

import {
  entryObjectKey,
  isGuardRejection,
  tagRecordUpdate,
  type CacheEntryFile,
  type TagRecord,
} from "@ocel/next-cache";

export type { CacheEntryFile, TagRecord } from "@ocel/next-cache";

export interface CacheStore {
  readEntry(key: string): Promise<CacheEntryFile | null>;
  writeEntry(key: string, entry: CacheEntryFile): Promise<void>;
  readFetch(hash: string): Promise<CacheEntryFile | null>;
  writeFetch(hash: string, entry: CacheEntryFile): Promise<void>;
  writeTags(tags: string[], record: TagRecord): Promise<void>;
}

function env(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`ocel cache handler: ${name} is not set`);
  return value;
}

async function streamToString(body: any): Promise<string> {
  if (typeof body?.transformToString === "function") {
    return body.transformToString();
  }
  const chunks: Buffer[] = [];
  for await (const chunk of body) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks).toString("utf8");
}

export function awsCacheStore(): CacheStore {
  const prefix = env("OCEL_ISR_PREFIX");
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");

  const provider = providerObjectStore();

  const ddb = new DynamoDBClient({});

  const objectKey = (key: string) => {
    const addressed = entryObjectKey(prefix, key);
    if (addressed === null) {
      throw new Error(
        `ocel cache handler: cache key ${JSON.stringify(key)} is not addressable inside ${prefix}`,
      );
    }
    return addressed;
  };
  const fetchKey = (hash: string) => `${prefix}/fetch-cache/${hash}.cache.json`;

  async function read(
    from: ObjectStore,
    key: string,
  ): Promise<CacheEntryFile | null> {
    try {
      const out = await from.client.send(
        new GetObjectCommand({ Bucket: from.bucket, Key: key }),
      );
      return JSON.parse(await streamToString(out.Body));
    } catch (err: any) {
      if (err?.name === "NoSuchKey" || err?.$metadata?.httpStatusCode === 404) {
        return null;
      }
      throw err;
    }
  }

  async function write(
    to: ObjectStore,
    key: string,
    entry: CacheEntryFile,
  ): Promise<void> {
    await to.client.send(
      new PutObjectCommand({
        Bucket: to.bucket,
        Key: key,
        Body: JSON.stringify(entry),
        ContentType: "application/json",
      }),
    );
  }

  const entries: EntryStore = entriesAdopted()
    ? isrEntryStore()
    : {
        read: async (key) => read(provider, objectKey(key)),
        write: async (key, entry) => write(provider, objectKey(key), entry),
      };

  return {
    readEntry: (key) => entries.read(key),
    writeEntry: (key, entry) => entries.write(key, entry),
    readFetch: (hash) => read(provider, fetchKey(hash)),
    writeFetch: (hash, entry) => write(provider, fetchKey(hash), entry),

    async writeTags(tags, record) {
      if (record.stale === undefined && record.expired === undefined) return;
      const writtenAt = Date.now();

      await Promise.all(
        tags.map(async (tag) => {
          try {
            await ddb.send(
              new UpdateItemCommand(
                tagRecordUpdate(table, tagNamespace, tag, { ...record, writtenAt }),
              ),
            );
          } catch (err) {
            if (!isGuardRejection(err)) throw err;
          }
        }),
      );
    },
  };
}
