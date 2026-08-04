import { DynamoDBClient, UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import { GetObjectCommand, PutObjectCommand } from "@aws-sdk/client-s3";

import { isrEntryStore, type EntryStore } from "./isr-writer.mjs";
import {
  entriesAdopted,
  providerObjectStore,
  type ObjectStore,
} from "./object-store.mjs";

// The entry and tag-record shapes are the shared ISR contract — the same the
// edge worker reads — so they live in @ocel/next-cache and are re-exported here
// for the handler and its tests.
import {
  entryObjectKey,
  isGuardRejection,
  tagRecordUpdate,
  type CacheEntryFile,
  type TagRecord,
} from "@ocel/next-cache";

export type { CacheEntryFile, TagRecord } from "@ocel/next-cache";

// CacheStore is the handler's whole view of its backing services, so the cache
// semantics can be exercised without reaching AWS.
export interface CacheStore {
  readEntry(key: string): Promise<CacheEntryFile | null>;
  writeEntry(key: string, entry: CacheEntryFile): Promise<void>;
  // Fetch entries are a separate backend, not a flavour of the same one: they
  // hold upstream response bodies, which are origin-private and must never reach
  // an adopted edge store the way route entries deliberately do. Separate
  // methods keep that boundary in the type rather than inside an `if`. This is
  // the canonical statement of that rule; the sites enforcing it point here.
  readFetch(hash: string): Promise<CacheEntryFile | null>;
  writeFetch(hash: string, entry: CacheEntryFile): Promise<void>;
  // Write-only: nothing on the read path asks the table what a tag's state is.
  // The raise goes here, the stream carries it to the publisher, and every
  // reader — this handler included — learns of it from the published snapshot
  // through the shared tag clock.
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

// awsCacheStore binds entries to whichever object store this substrate adopted
// and tag writes to the account-global state table. Keys are namespaced by the
// deploy's <env>/<project>/<app>/<build> prefix, which is also what the
// function's IAM policy is scoped to — so a key built outside the namespace
// fails closed rather than reading another app's cache.
export function awsCacheStore(): CacheStore {
  const prefix = env("OCEL_ISR_PREFIX");
  const table = env("OCEL_STATE_TABLE");
  const tagNamespace = env("OCEL_ISR_TAG_NAMESPACE");

  // The account's own bucket under the function's own role. It always holds the
  // fetch entries, whose bodies are origin-private and must never follow route
  // entries into a third party's store, and it holds the route entries too when
  // no store was adopted.
  const provider = providerObjectStore();

  const ddb = new DynamoDBClient({});

  // The same grammar the writer worker validates against, since it is the same
  // function: a key one accepts and the other refuses is a route that never
  // caches. A key that cannot be addressed inside this deploy's prefix is a bug
  // in the caller, so it throws here rather than reaching either store.
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

  // Where route entries live, as one seam whichever side of the writer they are
  // on. The writer worker holds the adopted cache store and nothing else, so an
  // adopted store and a writer are one decision and not two: every route entry,
  // read as well as written, travels through the writer, and this function holds
  // no R2 credential for entries at all. A deploy with a store and no writer
  // would write to the provider's bucket and read from the edge's — a miss on
  // every request and a re-render on every miss — so isrEntryStore() throws here
  // rather than let it degrade silently. The unadopted arm is the rollback for
  // the whole colocation: no edge, no writer, entries where they always were.
  const entries: EntryStore = entriesAdopted()
    ? isrEntryStore()
    : {
        // Async so a refused key rejects rather than throwing at the call, which
        // is what every caller of a Promise-returning method is written against.
        read: async (key) => read(provider, objectKey(key)),
        write: async (key, entry) => write(provider, objectKey(key), entry),
      };

  return {
    readEntry: (key) => entries.read(key),
    writeEntry: (key, entry) => entries.write(key, entry),
    readFetch: (hash) => read(provider, fetchKey(hash)),
    writeFetch: (hash, entry) => write(provider, fetchKey(hash), entry),

    // Merges rather than replaces. Next's own revalidateTag spreads the existing
    // record before applying its updates, so marking a tag stale must not drop an
    // expiry set earlier — a lost `expired` silently makes an invalidated tag
    // look fresh again and resurrects stale content.
    //
    // Indexed on the way through, under the same update the `use cache` tier
    // writes. An app on the classic ISR model has no `use cache` anywhere, so
    // nothing else ever writes its tags: an unindexed row here is an
    // invalidation no delta replica can see. Doing it here rather than relying
    // on Next fanning revalidateTag out to the plural handlers is what keeps
    // that true across framework versions.
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
            // Next hands revalidateTag through with no try/catch, so a rejected
            // guard must not fail the request: it only means another writer
            // already recorded a stricter invalidation for this event.
            if (!isGuardRejection(err)) throw err;
          }
        }),
      );
    },
  };
}
