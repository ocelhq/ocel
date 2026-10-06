import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";
import type { ScheduleRefresh } from "@framework/next-runtime/refresh";
import {
  dispatchesAtOrigin,
  finishBeforeResponseMs,
  readPortBind,
} from "@framework/node-runtime/host";
import { newGcpCacheStore } from "./cache-store.mjs";
import { newCdnPurge, withCdnPurge } from "./cdn-purge.mjs";
import { cloudCdnRelease, cloudCdnShapedTagsPerObject } from "./cloud-cdn.mjs";
import { newCloudStorage } from "./cloud-storage.mjs";
import { newGcpDispatchInvoke } from "./dispatch-host.mjs";
import { newFirestore } from "./firestore.mjs";
import { newInstanceCacheStore, newInstanceUseCacheStore } from "./instance-stores.mjs";
import { readRefreshEndpoint } from "./refresh-endpoint.mjs";
import { type RefreshEnv, readRefreshEnv } from "./refresh-env.mjs";
import { newRefreshQueue } from "./refresh-queue.mjs";
import { newFirestoreTagRecords } from "./tag-records.mjs";
import { newGcpUseCacheStore } from "./use-cache-store.mjs";

const MB = 1024 * 1024;

const memoryVar = "OCEL_FUNCTION_MEMORY_MB";

export function newGcpNextHost(env: NodeJS.ProcessEnv): NextHost {
  const refreshSecret = env.OCEL_REFRESH_SECRET;
  delete env.OCEL_REFRESH_SECRET;
  const refresh = readRefreshEnv(env, refreshSecret);
  const memoryMb = Number(env[memoryVar]);
  const cache = newInstanceCache(instanceCacheBytes(memoryMb > 0 ? memoryMb * MB : undefined));
  const bucket = env.OCEL_ISR_BUCKET;
  const objectPrefix = env.OCEL_ISR_OBJECT_PREFIX;
  const isrPrefix = env.OCEL_ISR_PREFIX;
  const tagDatabase = env.OCEL_TAG_DATABASE;
  const urlMap = env.OCEL_CDN_URL_MAP;
  const release = cloudCdnRelease(env);
  if (urlMap && release === null) {
    throw new Error(
      "ocel: OCEL_CDN_URL_MAP names a url map to purge, and this service is told no release its responses are tagged with",
    );
  }
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
          tags:
            urlMap && release !== null
              ? {
                  ...records,
                  publish: withCdnPurge(records.publish, newCdnPurge({ urlMap, release })),
                }
              : records,
        }
      : undefined;
  const newCacheStore = async () =>
    shared
      ? newGcpCacheStore(shared.storage, shared.objectPrefix, shared.tags.publish)
      : newInstanceCacheStore(cache);
  return {
    bind: readPortBind(env),
    cacheTagsPerObject: cloudCdnShapedTagsPerObject,
    instanceCache: cache,
    newCacheStore,
    newUseCacheStore: async () =>
      shared
        ? newGcpUseCacheStore(shared.storage, shared.objectPrefix, shared.tags)
        : newInstanceUseCacheStore(cache),
    newDispatchInvoke: async (localOrigin) =>
      newGcpDispatchInvoke(
        localOrigin,
        env,
        readRefreshEndpoint(refresh, localOrigin, async (key) =>
          (await newCacheStore()).readEntry(key),
        ),
      ),
    scheduleRefresh: readRefreshQueue(env, refresh),
  };
}

function readRefreshQueue(
  env: NodeJS.ProcessEnv,
  refresh: RefreshEnv | undefined,
): ScheduleRefresh | undefined {
  if (!refresh) {
    if (dispatchesAtOrigin(env) && finishBeforeResponseMs(env) > 0 && env.OCEL_ISR_PREFIX) {
      throw new Error(
        "ocel: a Next service billed per request refreshes stale pages through a Cloud Tasks queue, and its deploy named none",
      );
    }
    return undefined;
  }
  return newRefreshQueue({ ...refresh, endpoint: env.OCEL_TASKS_ENDPOINT });
}
