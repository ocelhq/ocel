import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";

const MB = 1024 * 1024;

const cloudFrontTagsPerObject = 50;

export function newAwsNextHost(env: NodeJS.ProcessEnv): NextHost {
  const memoryMb = Number(env.AWS_LAMBDA_FUNCTION_MEMORY_SIZE);
  return {
    newCacheStore: async () => (await import("./cache-store.mjs")).newAwsCacheStore(),
    newUseCacheStore: async () => {
      const { newAwsTagPublisher } = await import("./tag-snapshot-store.mjs");
      return (await import("./use-cache-store.mjs")).newAwsUseCacheStore(newAwsTagPublisher());
    },
    newDispatchInvoke: async (localOrigin) =>
      (await import("./dispatch-host.mjs")).newAwsDispatchInvoke(localOrigin),
    cacheTagsPerObject: cloudFrontTagsPerObject,
    functionDir: env.LAMBDA_TASK_ROOT,
    instanceCache: newInstanceCache(instanceCacheBytes(memoryMb > 0 ? memoryMb * MB : undefined)),
  };
}
