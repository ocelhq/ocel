import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";
import { readPortBind } from "@framework/node-runtime/host";
import { openPurgeFor } from "../cache/purge-target.mjs";
import { newGcpCacheStore } from "./cache-store.mjs";
import { cloudCdnShapedTagsPerObject } from "./cloud-cdn.mjs";
import { newCloudStorage } from "./cloud-storage.mjs";
import { newGcpDispatchInvoke } from "./dispatch-host.mjs";
import { newFirestore } from "./firestore.mjs";
import { newImageEndpointInvoke } from "./image-endpoint.mjs";
import { newInstanceCacheStore, newInstanceUseCacheStore } from "./instance-stores.mjs";
import { newFirestoreTagRecords, withCdnPurge } from "./tag-records.mjs";
import { newGcpUseCacheStore } from "./use-cache-store.mjs";

const MB = 1024 * 1024;

const memoryVar = "OCEL_FUNCTION_MEMORY_MB";

export function newGcpNextHost(env: NodeJS.ProcessEnv): NextHost {
  const memoryMb = Number(env[memoryVar]);
  const cache = newInstanceCache(instanceCacheBytes(memoryMb > 0 ? memoryMb * MB : undefined));
  const bucket = env.OCEL_ISR_BUCKET;
  const objectPrefix = env.OCEL_ISR_OBJECT_PREFIX;
  const isrPrefix = env.OCEL_ISR_PREFIX;
  const tagDatabase = env.OCEL_TAG_DATABASE;
  const purge = openPurgeFor(env);
  const records =
    bucket && objectPrefix && isrPrefix && tagDatabase
      ? newFirestoreTagRecords(
          newFirestore({ database: tagDatabase, endpoint: env.OCEL_FIRESTORE_ENDPOINT }),
          isrPrefix,
        )
      : undefined;
  const shared =
    bucket && objectPrefix && records
      ? {
          objectPrefix,
          storage: newCloudStorage({ bucket, endpoint: env.OCEL_STORAGE_ENDPOINT }),
          tags: purge ? { ...records, publish: withCdnPurge(records.publish, purge) } : records,
        }
      : undefined;
  const newCacheStore = async () =>
    shared ? newGcpCacheStore(shared.storage, shared.objectPrefix) : newInstanceCacheStore(cache);
  return {
    bind: readPortBind(env),
    cacheTagsPerObject: cloudCdnShapedTagsPerObject,
    instanceCache: cache,
    newCacheStore,
    newUseCacheStore: async () =>
      shared
        ? newGcpUseCacheStore(shared.storage, shared.objectPrefix, shared.tags)
        : newInstanceUseCacheStore(cache),
    newOriginInvoke: async (next) => newImageEndpointInvoke(next, env),
    newDispatchInvoke: async (localOrigin) => newGcpDispatchInvoke(localOrigin, env),
  };
}
