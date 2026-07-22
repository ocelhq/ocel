// Request-time Deployment resolution (ADR 0002): the frozen generic worker no
// longer bakes in a manifest/function-URL map/tag namespace at deploy time —
// it resolves its app's active Deployment from the deployments store on each
// request, through a service binding to the deployments-store worker.
//
// One `activeRecord` call folds pointer read and record read into a single
// round trip. Each isolate caches the resolved record at a short TTL — the
// upper bound on how long a rollback takes to reach this isolate. On refresh it
// sends the build id it already holds, so an unchanged Deployment revalidates
// cheaply: the store echoes the build id back and omits the (potentially large)
// record, which stays served from cache until the build actually moves.

// Mirrors workers/deployments-store's DeploymentRecord (src/store.ts). Kept as
// a local shape rather than a cross-worker import, matching how this worker
// already types every other binding (ObjectStoreReader, StorageLike, …) as the
// subset it needs rather than naming the edge on the other side.
export interface DeploymentRecord {
  app: string;
  buildId: string;
  routingManifest: unknown;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  createdAt: number;
  edgeWorkers?: unknown;
}

// Mirrors workers/deployments-store's ActiveRecordResult (src/store.ts).
export type ActiveRecordResult =
  | { kind: "no-pointer" }
  | { kind: "unchanged"; buildId: string }
  | { kind: "record"; buildId: string; record: DeploymentRecord }
  | { kind: "dangling"; buildId: string };

// The deployments-store worker's RPC surface as seen through a Cloudflare
// service binding (ADR 0002): unauthenticated — the binding itself, reachable
// only from another Worker in the same account, is the trust boundary.
export interface DeploymentsBinding {
  activeRecord(
    slug: string,
    app: string,
    knownBuildId?: string,
  ): Promise<ActiveRecordResult>;
}

export interface DeploymentsDeps {
  binding: DeploymentsBinding;
  // The project slug addressing its own instance in the shared
  // deployments-store worker (env.OCEL_SLUG).
  slug: string;
  app: string;
  // Injectable so TTL expiry never depends on wall-clock time. Milliseconds.
  now?: () => number;
}

export type DeploymentResolution =
  | { kind: "found"; record: DeploymentRecord }
  // No active-deployment pointer has ever been written for this app.
  | { kind: "not-found" }
  // The store is unreachable and no cached Deployment can stand in.
  | { kind: "unavailable" };

// Exactly the upper bound ADR 0002 sets on rollback propagation.
const RECORD_TTL_MS = 5_000;

interface CacheEntry {
  buildId: string;
  record: DeploymentRecord;
  at: number;
}

// Keyed by the binding, which is one stable object for the life of an
// isolate — the same idiom interception.ts's entryMemo uses, so state never
// leaks between isolates or between tests. An isolate only ever serves one app,
// so the inner map holds a single entry in practice; it is keyed by app anyway
// so nothing breaks if that ever changes.
const recordCache = new WeakMap<DeploymentsBinding, Map<string, CacheEntry>>();

function cacheMap(binding: DeploymentsBinding): Map<string, CacheEntry> {
  let map = recordCache.get(binding);
  if (!map) recordCache.set(binding, (map = new Map()));
  return map;
}

// resolveDeployment resolves this app's active Deployment through a single
// `activeRecord` call, cached in-isolate at a 5s TTL. It never throws — a store
// error becomes "unavailable" unless a cached Deployment can stand in.
export async function resolveDeployment(
  deps: DeploymentsDeps,
): Promise<DeploymentResolution> {
  const now = (deps.now ?? Date.now)();
  const cache = cacheMap(deps.binding);
  const cached = cache.get(deps.app);

  if (cached && now - cached.at < RECORD_TTL_MS) {
    return { kind: "found", record: cached.record };
  }

  let result: ActiveRecordResult;
  try {
    result = await deps.binding.activeRecord(deps.slug, deps.app, cached?.buildId);
  } catch {
    // Store unreachable: serve the last known Deployment, however stale — a
    // cold isolate has none, and can only report unavailable.
    if (cached) return { kind: "found", record: cached.record };
    return { kind: "unavailable" };
  }

  switch (result.kind) {
    case "no-pointer":
      return { kind: "not-found" };
    case "unchanged":
      // A store only answers "unchanged" to a build id we sent, so a cache
      // entry is always present; refresh its freshness and serve it.
      cache.set(deps.app, { ...cached!, at: now });
      return { kind: "found", record: cached!.record };
    case "record":
      cache.set(deps.app, {
        buildId: result.buildId,
        record: result.record,
        at: now,
      });
      return { kind: "found", record: result.record };
    case "dangling":
      // The pointer names a build the store holds no record for. Prune never
      // deletes the active Deployment, so this should never happen on a healthy
      // store; treat it as unavailable rather than guessing.
      return { kind: "unavailable" };
  }
}
