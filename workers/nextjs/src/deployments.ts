// Request-time Deployment resolution (ADR 0002): the frozen generic worker no
// longer bakes in a manifest/function-URL map/tag namespace at deploy time —
// it resolves its app's active Deployment from the deployments store on each
// request, through a service binding to the deployments-store worker.
//
// Caching is split by mutability. A Deployment record is immutable (keyed by
// build id), so it is memoized indefinitely behind a bounded per-isolate LRU;
// the active-deployment pointer changes on every promotion/rollback, so it
// carries a short TTL — the upper bound on how long a rollback takes to reach
// this isolate.
import { lruSet } from "./lru";

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

// The deployments-store worker's RPC surface as seen through a Cloudflare
// service binding (ADR 0002): unauthenticated — the binding itself, reachable
// only from another Worker in the same account, is the trust boundary.
export interface DeploymentsBinding {
  activeBuildId(slug: string, app: string): Promise<string | undefined>;
  record(
    slug: string,
    app: string,
    buildId: string,
  ): Promise<DeploymentRecord | undefined>;
}

export interface DeploymentsDeps {
  binding: DeploymentsBinding;
  // The project slug addressing its own instance in the shared
  // deployments-store worker (env.OCEL_SLUG).
  slug: string;
  app: string;
  // Injectable so pointer-TTL expiry never depends on wall-clock time.
  // Milliseconds.
  now?: () => number;
}

export type DeploymentResolution =
  | { kind: "found"; record: DeploymentRecord }
  // No active-deployment pointer has ever been written for this app.
  | { kind: "not-found" }
  // The store is unreachable and no cached Deployment can stand in.
  | { kind: "unavailable" };

// Exactly the upper bound ADR 0002 sets on rollback propagation.
const POINTER_TTL_MS = 5_000;
// Bounds isolate memory: an isolate only ever serves one app, so this is a
// bound on how many distinct build ids of that app stay warm at once (e.g.
// across a rollback and forward again).
const RECORD_CACHE_MAX = 16;

interface PointerEntry {
  buildId: string | undefined;
  at: number;
}

// Keyed by the binding, which is one stable object for the life of an
// isolate — the same idiom interception.ts's entryMemo uses, so state never
// leaks between isolates or between tests.
const pointerCache = new WeakMap<DeploymentsBinding, Map<string, PointerEntry>>();
const recordCache = new WeakMap<DeploymentsBinding, Map<string, DeploymentRecord>>();

function pointerMap(binding: DeploymentsBinding): Map<string, PointerEntry> {
  let map = pointerCache.get(binding);
  if (!map) pointerCache.set(binding, (map = new Map()));
  return map;
}

function recordMap(binding: DeploymentsBinding): Map<string, DeploymentRecord> {
  let map = recordCache.get(binding);
  if (!map) recordCache.set(binding, (map = new Map()));
  return map;
}

function recordKey(app: string, buildId: string): string {
  return `${app}/${buildId}`;
}

// resolveDeployment resolves this app's active Deployment: the pointer first
// (isolate-cached at a 5s TTL), then the record it names (isolate-cached
// indefinitely, since records never change once written). It never throws —
// a store error becomes "unavailable" unless a cached answer can stand in.
export async function resolveDeployment(
  deps: DeploymentsDeps,
): Promise<DeploymentResolution> {
  const now = (deps.now ?? Date.now)();
  const pointers = pointerMap(deps.binding);
  const records = recordMap(deps.binding);

  const cachedPointer = pointers.get(deps.app);
  let buildId: string | undefined;

  if (cachedPointer && now - cachedPointer.at < POINTER_TTL_MS) {
    buildId = cachedPointer.buildId;
  } else {
    try {
      buildId = await deps.binding.activeBuildId(deps.slug, deps.app);
      pointers.set(deps.app, { buildId, at: now });
    } catch {
      // Store unreachable: fall back to the last known pointer, however
      // stale — a cold isolate has none, and can only report unavailable.
      if (!cachedPointer) return { kind: "unavailable" };
      buildId = cachedPointer.buildId;
    }
  }

  if (!buildId) return { kind: "not-found" };

  const key = recordKey(deps.app, buildId);
  const cachedRecord = records.get(key);
  if (cachedRecord) return { kind: "found", record: cachedRecord };

  try {
    const record = await deps.binding.record(deps.slug, deps.app, buildId);
    // The pointer names a build the store holds no record for. Prune never
    // deletes the active Deployment, so this should never happen on a
    // healthy store; treat it as unavailable rather than guessing.
    if (!record) return { kind: "unavailable" };
    lruSet(records, key, record, RECORD_CACHE_MAX);
    return { kind: "found", record };
  } catch {
    return { kind: "unavailable" };
  }
}
