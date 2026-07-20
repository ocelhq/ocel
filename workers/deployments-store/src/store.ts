// The deployments store's storage-class logic: pure key/value operations over
// one project's Durable Object storage. Kept separate from the DO class
// (deployments-do.ts) and the HTTP/RPC surface (index.ts) so it can be
// exercised directly against a real DO instance's storage in tests, the same
// way workers/nextjs's cache.ts is exercised against the real workerd Cache.

// The subset of DurableObjectStorage this module calls. A real ctx.storage
// satisfies it directly; nothing here names an edge.
export interface StorageLike {
  get<T = unknown>(key: string): Promise<T | undefined>;
  put<T = unknown>(key: string, value: T): Promise<void>;
  put(entries: Record<string, unknown>): Promise<void>;
  delete(key: string): Promise<boolean>;
  delete(keys: string[]): Promise<number>;
}

// A Deployment record: everything the frozen generic worker needs to serve
// one app's build that used to be baked into its per-deploy worker script.
// edgeWorkers is a reserved, unused slot for future deployment-owned edge
// workers (Next edge routes / middleware) — carried so adding that later
// needs no record migration.
export interface DeploymentRecord {
  app: string;
  buildId: string;
  routingManifest: unknown;
  functionUrls: Record<string, string>;
  tagNamespace: string;
  assetPrefix: string;
  createdAt: number;
  edgeWorkers?: unknown;
}

// One project-wide Promotion: a promotion id grouping the per-app build ids
// it made active. The caller (host) supplies promotionId and ts — the store
// never generates ids, it only records what it's told.
export interface Promotion {
  promotionId: string;
  ts: number;
  builds: Record<string, string>;
}

export interface HistoryEntry extends Promotion {
  active: boolean;
}

export interface PruneResult {
  keptPromotionIds: string[];
  removedPromotionIds: string[];
  // "app/buildId" pairs whose records were deleted, so the caller knows
  // exactly which underlying artifacts (stacks, R2 assets, ISR entries) it
  // still needs to reclaim.
  removedRecordKeys: string[];
}

const HISTORY_KEY = "history";
const ACTIVE_KEY = "active";
const VERSION_KEY = "versionStamp";

function recordKey(app: string, buildId: string): string {
  return `record:${app}/${buildId}`;
}

export async function putStaged(
  storage: StorageLike,
  record: DeploymentRecord,
): Promise<void> {
  // Only ever writes the (app, build id) record. The active pointer lives
  // under separate keys (active/history), so staging can never change what's
  // currently serving.
  await storage.put(recordKey(record.app, record.buildId), record);
}

export async function record(
  storage: StorageLike,
  app: string,
  buildId: string,
): Promise<DeploymentRecord | undefined> {
  return storage.get<DeploymentRecord>(recordKey(app, buildId));
}

export async function promote(
  storage: StorageLike,
  promotion: Promotion,
): Promise<void> {
  const existing = (await storage.get<Promotion[]>(HISTORY_KEY)) ?? [];
  // A single multi-key put() is applied by the storage layer as one atomic
  // write, so a crash mid-promote can never leave the active pointer naming a
  // promotion absent from history, or vice versa.
  await storage.put({
    [HISTORY_KEY]: [...existing, promotion],
    [ACTIVE_KEY]: promotion.promotionId,
  });
}

export async function activeBuildId(
  storage: StorageLike,
  app: string,
): Promise<string | undefined> {
  const activeId = await storage.get<string>(ACTIVE_KEY);
  if (!activeId) return undefined;
  const history = (await storage.get<Promotion[]>(HISTORY_KEY)) ?? [];
  return history.find((p) => p.promotionId === activeId)?.builds[app];
}

export async function history(storage: StorageLike): Promise<HistoryEntry[]> {
  const [entries, activeId] = await Promise.all([
    storage.get<Promotion[]>(HISTORY_KEY),
    storage.get<string>(ACTIVE_KEY),
  ]);
  // Stored oldest-first (promote appends); returned newest-first per the
  // acceptance criteria, with the active promotion marked.
  return [...(entries ?? [])]
    .reverse()
    .map((p) => ({ ...p, active: p.promotionId === activeId }));
}

export async function prune(
  storage: StorageLike,
  keepN: number,
): Promise<PruneResult> {
  const [entries, activeId] = await Promise.all([
    storage.get<Promotion[]>(HISTORY_KEY),
    storage.get<string>(ACTIVE_KEY),
  ]);
  const newestFirst = [...(entries ?? [])].reverse();

  // The keep window is the N most recent promotions; the active one is
  // pinned even if it falls outside that window, so pruning can never take
  // the live site down.
  const kept: Promotion[] = [];
  const removed: Promotion[] = [];
  for (const [i, p] of newestFirst.entries()) {
    if (i < keepN || p.promotionId === activeId) kept.push(p);
    else removed.push(p);
  }

  const removedRecordKeys = removed.flatMap((p) =>
    Object.entries(p.builds).map(([app, buildId]) => recordKey(app, buildId)),
  );

  await storage.put(HISTORY_KEY, [...kept].reverse());
  if (removedRecordKeys.length > 0) await storage.delete(removedRecordKeys);

  return {
    keptPromotionIds: kept.map((p) => p.promotionId),
    removedPromotionIds: removed.map((p) => p.promotionId),
    removedRecordKeys,
  };
}

export async function versionStamp(
  storage: StorageLike,
): Promise<string | undefined> {
  return storage.get<string>(VERSION_KEY);
}

export async function setVersionStamp(
  storage: StorageLike,
  version: string,
): Promise<void> {
  await storage.put(VERSION_KEY, version);
}
