import type { NextHost } from "@framework/next-runtime/host";
import { instanceCacheBytes, newInstanceCache } from "@framework/next-runtime/instance-cache";
import { readPortBind } from "@framework/node-runtime/host";

const MB = 1024 * 1024;

const memoryVar = "OCEL_FUNCTION_MEMORY_MB";

export function newGcpNextHost(env: NodeJS.ProcessEnv): NextHost {
  const memoryMb = Number(env[memoryVar]);
  return {
    bind: readPortBind(env),
    instanceCache: newInstanceCache(instanceCacheBytes(memoryMb > 0 ? memoryMb * MB : undefined)),
  };
}
