import { createHash } from "node:crypto";
import type { UseCacheStore } from "@framework/next-runtime/use-cache-store";
import type { CloudStorage } from "./cloud-storage.mjs";
import type { TagRecords } from "./tag-records.mjs";

export function newGcpUseCacheStore(
  storage: CloudStorage,
  objectPrefix: string,
  tags: TagRecords,
): UseCacheStore {
  const entryName = (key: string) =>
    `${objectPrefix}/use-cache/${createHash("sha256").update(key).digest("hex")}.json`;
  return {
    async readEntry(key) {
      const object = await storage.read(entryName(key));
      return object.status === "found" ? JSON.parse(object.body) : null;
    },
    async writeEntry(key, entry) {
      await storage.write(entryName(key), JSON.stringify(entry));
    },
    readTagSnapshot: (etag) => tags.read(etag),
    async writeTag(tag, record) {
      await tags.publish(tag, { stale: record.stale, expired: record.expired });
      return true;
    },
  };
}
