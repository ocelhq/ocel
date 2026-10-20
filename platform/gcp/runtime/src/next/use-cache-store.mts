import { createHash } from "node:crypto";
import type { UseCacheStore } from "@framework/next-runtime/use-cache-store";
import { objectKey, type StorageCoordinate } from "@platform/edge-contract/storage-key";
import type { CloudStorage } from "./cloud-storage.mjs";
import type { TagRecords } from "./tag-records.mjs";

export function newGcpUseCacheStore(
  storage: CloudStorage,
  coordinate: StorageCoordinate,
  tags: TagRecords,
): UseCacheStore {
  const entryName = (key: string) =>
    objectKey("use-cache", coordinate, `${createHash("sha256").update(key).digest("hex")}.json`);
  return {
    async readEntry(key) {
      const object = await storage.read(entryName(key));
      return object.status === "found" ? JSON.parse(object.body) : null;
    },
    async writeEntry(key, entry) {
      await storage.write(entryName(key), JSON.stringify(entry));
    },
    readTagSnapshot: (cursor) => tags.read(cursor),
    async writeTag(tag, record) {
      await tags.publish(tag, { stale: record.stale, expired: record.expired });
      return true;
    },
  };
}
