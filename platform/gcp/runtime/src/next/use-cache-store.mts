import type { UseCacheStore } from "@framework/next-runtime/use-cache-store";
import type { TagRecords } from "./tag-records.mjs";

export function newGcpUseCacheStore(entries: UseCacheStore, tags: TagRecords): UseCacheStore {
  return {
    readEntry: (key) => entries.readEntry(key),
    writeEntry: (key, entry) => entries.writeEntry(key, entry),
    readTagSnapshot: (etag) => tags.read(etag),
    async writeTag(tag, record) {
      await tags.publish(tag, { stale: record.stale, expired: record.expired });
      return true;
    },
  };
}
