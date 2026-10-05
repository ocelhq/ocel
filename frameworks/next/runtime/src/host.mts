import type { CacheStore } from "./cache-store.mjs";
import type { UseCacheStore } from "./use-cache-store.mjs";

export interface NextHost {
  newCacheStore?: () => Promise<CacheStore>;
  newUseCacheStore?: () => Promise<UseCacheStore>;
}

const hostKey = Symbol.for("ocel.next.host.v1");

const slots = globalThis as Record<symbol, NextHost | undefined>;

export function installNextHost(host: NextHost): void {
  slots[hostKey] = host;
}

export function getNextHost(): NextHost {
  return slots[hostKey] ?? {};
}
