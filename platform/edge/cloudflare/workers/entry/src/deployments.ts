import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import type { EdgeWorkers } from "./edge";
import { lruSet } from "./lru";

export interface DeploymentRecord {
  app: string;
  framework: string;
  identity: string;
  deploymentId: string;
  entry?: string;
  routingManifest?: RoutingManifest | null;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  isrWriteSecret?: string;
  createdAt: number;
  edgeWorkers?: EdgeWorkers;
  env?: Record<string, string>;
  envelope?: string;
  buildFingerprint?: string;
}

export type PointerRecordResult =
  | { kind: "no-pointer" }
  | { kind: "ambiguous-app" }
  | { kind: "unchanged"; identity: string }
  | { kind: "record"; identity: string; record: DeploymentRecord }
  | { kind: "dangling"; identity: string };

export interface DeploymentsBinding {
  readPointerRecord(args: {
    slug: string;
    app?: string;
    knownIdentity?: string;
  }): Promise<PointerRecordResult>;
  readLabelRecord(args: {
    slug: string;
    label: string;
    knownIdentity?: string;
  }): Promise<PointerRecordResult>;
}

export interface DeploymentsDeps {
  binding: DeploymentsBinding;
  slug: string;
  host: string;
  app?: string;
  label?: string;
  now?: () => number;
}

export type DeploymentResolution =
  | { kind: "found"; record: DeploymentRecord }
  | { kind: "not-found" }
  | { kind: "unavailable" };

const RECORD_TTL_MS = 5_000;
export const RECORD_CACHE_MAX = 64;

interface CacheEntry {
  identity: string;
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

function describeDeployScope(deps: DeploymentsDeps): string {
  if (deps.label !== undefined) return `${deps.slug}/${deps.label}`;
  if (deps.app) return `${deps.slug}/${deps.app}`;
  return deps.slug;
}

export async function resolveDeployment(deps: DeploymentsDeps): Promise<DeploymentResolution> {
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
    const knownIdentity = cached?.identity;
    result =
      deps.label === undefined
        ? await deps.binding.readPointerRecord({ slug: deps.slug, app: deps.app, knownIdentity })
        : await deps.binding.readLabelRecord({ slug: deps.slug, label: deps.label, knownIdentity });
  } catch (error) {
    const scope = describeDeployScope(deps);
    if (cached) {
      const ageSeconds = Math.round((now - cached.at) / 1000);
      console.error(
        `ocel: the deployments store did not answer for ${scope}; serving the record cached ${ageSeconds}s ago`,
        error,
      );
      return { kind: "found", record: cached.record };
    }
    console.error(`ocel: the deployments store did not answer for ${scope}; answering 503`, error);
    return { kind: "unavailable" };
  }

  switch (result.kind) {
    case "no-pointer":
    case "ambiguous-app":
      return { kind: "not-found" };
    case "unchanged":
      if (!cached) return { kind: "unavailable" };
      lruSet(cache, key, { ...cached, at: now }, RECORD_CACHE_MAX);
      return { kind: "found", record: cached.record };
    case "record":
      lruSet(
        cache,
        key,
        { identity: result.identity, record: result.record, at: now },
        RECORD_CACHE_MAX,
      );
      return { kind: "found", record: result.record };
    case "dangling":
      return { kind: "unavailable" };
  }
}
