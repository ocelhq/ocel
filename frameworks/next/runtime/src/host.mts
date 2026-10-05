import { dispatchesAtOrigin, type Invoke } from "@framework/node-runtime/host";
import type { CacheStore } from "./cache-store.mjs";
import type { UseCacheStore } from "./use-cache-store.mjs";

export interface NextHost {
  newCacheStore?: () => Promise<CacheStore>;
  newUseCacheStore?: () => Promise<UseCacheStore>;
  newDispatchInvoke?: (localOrigin: string) => Promise<Invoke>;
}

const hostKey = Symbol.for("ocel.next.host.v1");

const slots = globalThis as Record<symbol, NextHost | undefined>;

const isrPrefixVar = "OCEL_ISR_PREFIX";

export function installNextHost(host: NextHost): void {
  slots[hostKey] = host;
}

export function getNextHost(): NextHost {
  return slots[hostKey] ?? {};
}

export function refuseIncompleteHost(host: NextHost, env: NodeJS.ProcessEnv): Error | undefined {
  const missing: string[] = [];
  if (env[isrPrefixVar] && !host.newCacheStore) missing.push("cache store");
  if (env[isrPrefixVar] && !host.newUseCacheStore) missing.push("use-cache store");
  if (dispatchesAtOrigin(env) && !host.newDispatchInvoke) missing.push("dispatcher");
  if (missing.length === 0) return undefined;
  return new Error(`ocel: the Next runtime's host installed no ${missing.join(", no ")}`);
}
