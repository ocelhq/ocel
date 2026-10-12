import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { type CacheEntryFile, entryObjectKey } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import {
  edgeReadable,
  edgeReadableTagging,
  isNotFound,
  type ObjectStore,
  providerObjectStore,
  readBodyText,
  requireEnv,
} from "./object-store.mjs";

const EDGE_SERVED_KINDS = new Set(["APP_PAGE", "PAGES", "APP_ROUTE"]);

export function newAwsCacheStore(): CacheStore {
  const prefix = requireEnv("OCEL_ISR_PREFIX");

  const provider = providerObjectStore();

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

  async function write(
    to: ObjectStore,
    key: string,
    entry: CacheEntryFile,
    tagging?: string,
  ): Promise<void> {
    await to.client.send(
      new PutObjectCommand({
        Bucket: to.bucket,
        Key: key,
        Body: JSON.stringify(entry),
        ContentType: "application/json",
        ...(tagging !== undefined && { Tagging: tagging }),
      }),
    );
  }

  const tagsPages = edgeReadable();
  const taggingOf = (entry: CacheEntryFile) =>
    tagsPages && EDGE_SERVED_KINDS.has(entry.value?.kind) ? edgeReadableTagging : undefined;

  return {
    readEntry: async (key) => read(provider, objectKey(key)),
    writeEntry: async (key, entry) => write(provider, objectKey(key), entry, taggingOf(entry)),
    readFetch: (hash) => read(provider, fetchKey(hash)),
    writeFetch: (hash, entry) => write(provider, fetchKey(hash), entry),
  };
}
