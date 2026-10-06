import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";
import type { ScheduleRefresh } from "@framework/next-runtime/refresh";
import {
  dispatchesAtOrigin,
  finishBeforeResponseMs,
  readPortBind,
} from "@framework/node-runtime/host";
import { newGcpCacheStore } from "./cache-store.mjs";
import { newCloudStorage } from "./cloud-storage.mjs";
import { newGcpDispatchInvoke } from "./dispatch-host.mjs";
import { newFirestore } from "./firestore.mjs";
import { newInstanceCacheStore, newInstanceUseCacheStore } from "./instance-stores.mjs";
import { readRefreshEndpoint } from "./refresh-endpoint.mjs";
import { newFirestoreTagRecords } from "./tag-records.mjs";
import { newTaskRefresh } from "./task-refresh.mjs";
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
  const shared =
    bucket && objectPrefix && isrPrefix && tagDatabase
      ? {
          objectPrefix,
          storage: newCloudStorage({ bucket, endpoint: env.OCEL_STORAGE_ENDPOINT }),
          tags: newFirestoreTagRecords(
            newFirestore({ database: tagDatabase, endpoint: env.OCEL_FIRESTORE_ENDPOINT }),
            isrPrefix,
          ),
        }
      : undefined;
  const newCacheStore = async () =>
    shared
      ? newGcpCacheStore(shared.storage, shared.objectPrefix, shared.tags.publish)
      : newInstanceCacheStore(cache);
  return {
    bind: readPortBind(env),
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
        readRefreshEndpoint(env, localOrigin, async (key) =>
          (await newCacheStore()).readEntry(key),
        ),
      ),
    scheduleRefresh: readTaskRefresh(env),
  };
}

function readTaskRefresh(env: NodeJS.ProcessEnv): ScheduleRefresh | undefined {
  const url = env.OCEL_REFRESH_URL;
  if (!url) {
    if (dispatchesAtOrigin(env) && finishBeforeResponseMs(env) > 0 && env.OCEL_ISR_PREFIX) {
      throw new Error(
        "ocel: a Next service billed per request refreshes stale pages through a Cloud Tasks queue, and its deploy named none",
      );
    }
    return undefined;
  }
  for (const name of ["OCEL_REFRESH_QUEUE", "OCEL_REFRESH_ACCOUNT", "OCEL_ISR_PREFIX"]) {
    if (!env[name]) throw new Error(`ocel: OCEL_REFRESH_URL is set but ${name} is not`);
  }
  return newTaskRefresh({
    queue: env.OCEL_REFRESH_QUEUE!,
    account: env.OCEL_REFRESH_ACCOUNT!,
    url,
    isrPrefix: env.OCEL_ISR_PREFIX!,
    endpoint: env.OCEL_TASKS_ENDPOINT,
  });
}
