import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";
import { finishBeforeResponseMs, readPortBind } from "@framework/node-runtime/host";
import { newGcpDispatchInvoke } from "./dispatch-host.mjs";
import { newInstanceRefresh } from "./instance-refresh.mjs";
import { newInstanceCacheStore, newInstanceUseCacheStore } from "./instance-stores.mjs";

const renderOriginVar = "__NEXT_PRIVATE_ORIGIN";
const defaultRefreshTimeoutMs = 10_000;

function readInstanceOrigin(env: NodeJS.ProcessEnv): string {
  const origin = env[renderOriginVar];
  if (!origin)
    throw new Error("ocel: the Next runtime has not started the server a refresh renders on");
  return origin;
}

const MB = 1024 * 1024;

const memoryVar = "OCEL_FUNCTION_MEMORY_MB";

export function newGcpNextHost(env: NodeJS.ProcessEnv): NextHost {
  const memoryMb = Number(env[memoryVar]);
  const cache = newInstanceCache(instanceCacheBytes(memoryMb > 0 ? memoryMb * MB : undefined));
  return {
    bind: readPortBind(env),
    instanceCache: cache,
    newCacheStore: async () => newInstanceCacheStore(cache),
    newUseCacheStore: async () => newInstanceUseCacheStore(cache),
    newDispatchInvoke: async (localOrigin) => newGcpDispatchInvoke(localOrigin),
    scheduleRefresh: newInstanceRefresh(
      () => readInstanceOrigin(env),
      finishBeforeResponseMs(env) || defaultRefreshTimeoutMs,
    ),
  };
}
