import { type CacheEntryFile, entryObjectKey, type PublishTag } from "@framework/next-cache";
import type { CacheStore } from "@framework/next-runtime/cache-store";
import type { IsrWriterClient } from "@platform/edge-contract/isr-writer";
import type { CloudStorage } from "./cloud-storage.mjs";

export function newGcpCacheStore(
  storage: CloudStorage,
  objectPrefix: string,
  publish: PublishTag,
  pages?: Pick<IsrWriterClient, "readEntry" | "writeEntry">,
): CacheStore {
  const entryName = (key: string) => {
    const name = entryObjectKey(objectPrefix, key);
    if (name === null) {
      throw new Error(
        `ocel cache handler: cache key ${JSON.stringify(key)} is not addressable inside ${objectPrefix}`,
      );
    }
    return name;
  };
  const fetchName = (hash: string) => `${objectPrefix}/fetch-cache/${hash}.cache.json`;

  async function read(name: string): Promise<CacheEntryFile | null> {
    const object = await storage.read(name);
    return object.status === "found" ? JSON.parse(object.body) : null;
  }

  async function write(name: string, entry: CacheEntryFile): Promise<void> {
    await storage.write(name, JSON.stringify(entry));
  }

  return {
    readEntry: async (key) => (pages ? pages.readEntry(key) : read(entryName(key))),
    writeEntry: async (key, entry) =>
      pages ? pages.writeEntry(key, entry) : write(entryName(key), entry),
    readFetch: (hash) => read(fetchName(hash)),
    writeFetch: (hash, entry) => write(fetchName(hash), entry),

    async writeTags(tags, record) {
      if (record.stale === undefined && record.expired === undefined) return;
      await Promise.all(tags.map((tag) => publish(tag, record)));
    },
  };
}
