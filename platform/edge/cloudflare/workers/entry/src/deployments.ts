import type { EdgeWorkers } from "./edge";
import { lruSet } from "./lru";

export interface DeploymentRecord {
  app: string;
  framework: string;
  deploymentId: string;
  buildId: string;
  routingManifest: unknown;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  isrWriteSecret?: string;
  createdAt: number;
  edgeWorkers?: EdgeWorkers;
}

export type PointerRecordResult =
  | { kind: "no-pointer" }
  | { kind: "ambiguous-app" }
  | { kind: "unchanged"; deploymentId: string }
  | { kind: "record"; deploymentId: string; record: DeploymentRecord }
  | { kind: "dangling"; deploymentId: string };

export interface DeploymentsBinding {
  pointerRecord(args: {
    slug: string;
    app?: string;
    pointer?: string;
    knownDeploymentId?: string;
  }): Promise<PointerRecordResult>;
}

export interface DeploymentsDeps {
  binding: DeploymentsBinding;
  slug: string;
  host: string;
  app?: string;
  pointer?: string;
  now?: () => number;
}

export type DeploymentResolution =
  | { kind: "found"; record: DeploymentRecord }
  | { kind: "not-found" }
  | { kind: "unavailable" };

const RECORD_TTL_MS = 5_000;
export const RECORD_CACHE_MAX = 64;

interface CacheEntry {
  deploymentId: string;
  record: DeploymentRecord;
  at: number;
}

const recordCache = new WeakMap<DeploymentsBinding, Map<string, CacheEntry>>();

function cacheMap(binding: DeploymentsBinding): Map<string, CacheEntry> {
  let map = recordCache.get(binding);
  if (!map) recordCache.set(binding, (map = new Map()));
  return map;
}

function cacheKey(deps: DeploymentsDeps): string {
  return deps.host;
}

export async function resolveDeployment(
  deps: DeploymentsDeps,
): Promise<DeploymentResolution> {
  const now = (deps.now ?? Date.now)();
  const cache = cacheMap(deps.binding);
  const key = cacheKey(deps);
  const cached = cache.get(key);
  if (cached) lruSet(cache, key, cached, RECORD_CACHE_MAX);

  if (cached && now - cached.at < RECORD_TTL_MS) {
    return { kind: "found", record: cached.record };
  }

  let result: PointerRecordResult;
  try {
    result = await deps.binding.pointerRecord({
      slug: deps.slug,
      app: deps.app,
      pointer: deps.pointer,
      knownDeploymentId: cached?.deploymentId,
    });
  } catch {
    if (cached) return { kind: "found", record: cached.record };
    return { kind: "unavailable" };
  }

  switch (result.kind) {
    case "no-pointer":
    case "ambiguous-app":
      return { kind: "not-found" };
    case "unchanged":
      lruSet(cache, key, { ...cached!, at: now }, RECORD_CACHE_MAX);
      return { kind: "found", record: cached!.record };
    case "record":
      lruSet(
        cache,
        key,
        { deploymentId: result.deploymentId, record: result.record, at: now },
        RECORD_CACHE_MAX,
      );
      return { kind: "found", record: result.record };
    case "dangling":
      return { kind: "unavailable" };
  }
}
