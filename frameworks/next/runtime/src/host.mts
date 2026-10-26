import { type Bind, dispatchesAtOrigin, type Invoke } from "@framework/node-runtime/host";
import type { PurgeTags } from "@framework/node-runtime/tag-records";
import { isrVar } from "@platform/edge-contract/runtime-env";
import type { CacheStore } from "./cache-store.mjs";
import type { InstanceCache } from "./instance-cache.mjs";
import type { UseCacheStore } from "./use-cache-store.mjs";

export interface NextHost {
  newCacheStore?: () => Promise<CacheStore>;
  newUseCacheStore?: () => Promise<UseCacheStore>;
  newDispatchInvoke?: (localOrigin: string) => Promise<Invoke>;
  newOriginInvoke?: (next: Invoke) => Promise<Invoke>;
  edgePurge?: PurgeTags;
  cacheTagsPerObject?: number;
  functionDir?: string;
  instanceCache?: InstanceCache;
  bind?: Bind;
}

const hostKey = Symbol.for("ocel.next.host.v1");

const slots = globalThis as Record<symbol, NextHost | undefined>;

export function installNextHost(host: NextHost): void {
  slots[hostKey] = host;
}

export function getNextHost(): NextHost {
  return slots[hostKey] ?? {};
}

export function refuseIncompleteHost(host: NextHost, env: NodeJS.ProcessEnv): Error | undefined {
  const missing: string[] = [];
  const revalidates = env[isrVar] === "1";
  if (revalidates && !host.newCacheStore) missing.push("cache store");
  if (revalidates && !host.newUseCacheStore) missing.push("use-cache store");
  if (dispatchesAtOrigin(env) && !host.newDispatchInvoke) missing.push("dispatcher");
  if (missing.length === 0) return undefined;
  return new Error(`ocel: the Next runtime's host installed no ${missing.join(", no ")}`);
}
