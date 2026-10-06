import { DynamoDBClient, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { type CacheEntryFile, entryObjectKey, type PublishTag } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import { newIsrWriterClient } from "@platform/edge-contract/isr-writer";
import {
  entriesAdopted,
  isNotFound,
  type ObjectStore,
  providerObjectStore,
  readBodyText,
  requireEnv,
} from "./object-store.mjs";
import { isGuardRejection, tagRecordUpdate } from "./tag-index.mjs";

interface EntryStore {
  read(key: string): Promise<CacheEntryFile | null>;
  write(key: string, entry: CacheEntryFile): Promise<void>;
}

function openAdoptedEntryStore(isrPrefix: string): EntryStore {
  const endpoint = process.env.OCEL_ISR_WRITER_URL;
  const secret = process.env.OCEL_ISR_WRITER_SECRET;
  if (!endpoint || !secret) {
    throw new Error(
      "ocel cache handler: OCEL_ISR_WRITER_URL and OCEL_ISR_WRITER_SECRET must both be set " +
        "when this deploy reads its ISR entries from an adopted cache store; " +
        "re-run `ocel bootstrap production` and redeploy",
    );
  }
  const client = newIsrWriterClient({ endpoint, isrPrefix, secret });
  return {
    read: (key) => client.readEntry(key),
    write: (key, entry) => client.writeEntry(key, entry),
  };
}

export function newAwsCacheStore(publish: PublishTag): CacheStore {
  const prefix = requireEnv("OCEL_ISR_PREFIX");
  const table = requireEnv("OCEL_STATE_TABLE");
  const tagNamespace = requireEnv("OCEL_ISR_TAG_NAMESPACE");

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

  async function read(from: ObjectStore, key: string): Promise<CacheEntryFile | null> {
    try {
      const out = await from.client.send(new GetObjectCommand({ Bucket: from.bucket, Key: key }));
      return JSON.parse(await readBodyText(out.Body));
    } catch (err: any) {
      if (isNotFound(err)) return null;
      throw err;
    }
  }

  async function write(to: ObjectStore, key: string, entry: CacheEntryFile): Promise<void> {
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
    ? openAdoptedEntryStore(prefix)
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

      const indexed = async (tag: string) => {
        try {
          await ddb.send(
            new UpdateItemCommand(
              tagRecordUpdate(table, tagNamespace, tag, { ...record, writtenAt }),
            ),
          );
        } catch (err) {
          if (!isGuardRejection(err)) throw err;
        }
      };

      await Promise.all(tags.flatMap((tag) => [indexed(tag), publish(tag, record)]));
    },
  };
}
