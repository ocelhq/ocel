import type { ObjectStore } from "@framework/next-image-optimizer/store";
import type { AssetBucket, AssetObject } from "@framework/next-router/assets";
import type { CloudStorage } from "./cloud-storage.mjs";

const assetsStore = "assets/";

export function newCloudStorageAssetBucket(storage: CloudStorage): AssetBucket {
  return {
    async get(key): Promise<AssetObject | null> {
      const object = await storage.open(assetsStore + key);
      if (object.status === "absent") return null;
      return { body: object.body, httpEtag: object.etag };
    },
  };
}

export function newCloudStorageObjectStore(storage: CloudStorage): ObjectStore {
  return {
    async get(key, limit) {
      const object = await storage.open(assetsStore + key);
      if (object.status === "absent") return undefined;
      const refuse = async (size: number) => {
        await object.body?.cancel().catch(() => undefined);
        throw new Error(`object ${key} holds ${size} bytes`);
      };
      if (object.size !== null && object.size > limit) return refuse(object.size);
      const bytes = new Uint8Array(await new Response(object.body).arrayBuffer());
      if (bytes.byteLength > limit) return refuse(bytes.byteLength);
      return { bytes, cacheControl: null, etag: null };
    },
  };
}
