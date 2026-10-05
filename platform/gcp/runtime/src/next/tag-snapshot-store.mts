import {
  readableSnapshot,
  type StoredTagSnapshot,
  type TagSnapshot,
  type TagSnapshotStore,
  tagSnapshotKey,
} from "@framework/next-cache";
import type { CloudStorage } from "./cloud-storage.mjs";

export function newCloudStorageTagSnapshotStore(
  storage: CloudStorage,
  objectPrefix: string,
): TagSnapshotStore {
  const name = tagSnapshotKey(objectPrefix);
  return {
    async read(): Promise<StoredTagSnapshot | null> {
      const read = await storage.read(name);
      if (read.status !== "found") return null;
      const snapshot = readableSnapshot(JSON.parse(read.body));
      if (snapshot === null) {
        throw new Error(
          `ocel: tag snapshot ${name} is not a version this publisher can merge into`,
        );
      }
      return { snapshot, etag: read.generation };
    },

    async write(snapshot: TagSnapshot, prior: StoredTagSnapshot): Promise<boolean> {
      const written = await storage.write(name, JSON.stringify(snapshot), {
        ifGenerationMatch: prior.etag!,
      });
      return written.status === "written";
    },
  };
}
