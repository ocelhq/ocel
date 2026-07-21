// The deployments store's storage-class logic: SQL operations over one
// project's Durable Object SQLite storage. Kept separate from the DO class
// (deployments-do.ts) and the HTTP/RPC surface (index.ts) so it can be
// exercised directly against a real DO instance's storage in tests, the same
// way workers/nextjs's cache.ts is exercised against the real workerd Cache.

// The subset of DurableObjectStorage this module calls: the synchronous SQL
// API plus transactionSync for the multi-statement writes (promote, prune)
// that must land atomically. A real ctx.storage satisfies it structurally;
// nothing here names an edge.
export interface SqlStore {
  sql: SqlStorage;
  transactionSync<T>(closure: () => T): T;
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
  assetPrefix: string;
  isrPrefix: string;
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
  // "record:<app>/<buildId>" keys whose records were deleted, so the caller
  // knows exactly which underlying artifacts (stacks, R2 assets, ISR entries)
  // it still needs to reclaim. Kept in this prefixed form because the Go host
  // (cloud/aws/deploy/prune.go ReclaimTargets) parses it verbatim.
  removedRecordKeys: string[];
}

const ACTIVE_KEY = "active";
const VERSION_KEY = "versionStamp";

function recordKey(app: string, buildId: string): string {
  return `record:${app}/${buildId}`;
}

// Called once from the DO constructor, before any method runs. CREATE TABLE
// IF NOT EXISTS is idempotent, so re-running on every cold start is safe.
export function ensureSchema(store: SqlStore): void {
  store.sql.exec(
    `CREATE TABLE IF NOT EXISTS records (
       app TEXT NOT NULL,
       build_id TEXT NOT NULL,
       data TEXT NOT NULL,
       PRIMARY KEY (app, build_id)
     );
     CREATE TABLE IF NOT EXISTS promotions (
       promotion_id TEXT PRIMARY KEY,
       ts INTEGER NOT NULL,
       builds TEXT NOT NULL,
       seq INTEGER NOT NULL
     );
     CREATE TABLE IF NOT EXISTS meta (
       key TEXT PRIMARY KEY,
       value TEXT NOT NULL
     );`,
  );
}

function getMeta(store: SqlStore, key: string): string | undefined {
  const row = store.sql
    .exec<{ value: string }>(`SELECT value FROM meta WHERE key = ?`, key)
    .toArray()[0];
  return row?.value;
}

function setMeta(store: SqlStore, key: string, value: string): void {
  store.sql.exec(
    `INSERT INTO meta (key, value) VALUES (?, ?)
     ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
    key,
    value,
  );
}

export function putStaged(store: SqlStore, record: DeploymentRecord): void {
  // Only ever writes the (app, build id) record. The active pointer lives in
  // the meta table, so staging can never change what's currently serving.
  store.sql.exec(
    `INSERT INTO records (app, build_id, data) VALUES (?, ?, ?)
     ON CONFLICT(app, build_id) DO UPDATE SET data = excluded.data`,
    record.app,
    record.buildId,
    JSON.stringify(record),
  );
}

export function record(
  store: SqlStore,
  app: string,
  buildId: string,
): DeploymentRecord | undefined {
  const row = store.sql
    .exec<{ data: string }>(
      `SELECT data FROM records WHERE app = ? AND build_id = ?`,
      app,
      buildId,
    )
    .toArray()[0];
  return row ? (JSON.parse(row.data) as DeploymentRecord) : undefined;
}

export function promote(store: SqlStore, promotion: Promotion): void {
  // Bumping seq to a fresh maximum on every promote — including a rollback's
  // re-promote of an existing id — is what makes the promoted entry the newest
  // in history(), so the previously-active promotion becomes the "immediately
  // previous" one that a further rollback rolls back to. The whole thing runs
  // in one transaction, so a crash mid-promote can never leave the active
  // pointer naming a promotion that isn't in the table, or vice versa.
  store.transactionSync(() => {
    const nextSeq = store.sql
      .exec<{ n: number }>(`SELECT COALESCE(MAX(seq), 0) + 1 AS n FROM promotions`)
      .one().n;
    store.sql.exec(
      `INSERT INTO promotions (promotion_id, ts, builds, seq) VALUES (?, ?, ?, ?)
       ON CONFLICT(promotion_id) DO UPDATE SET
         ts = excluded.ts, builds = excluded.builds, seq = excluded.seq`,
      promotion.promotionId,
      promotion.ts,
      JSON.stringify(promotion.builds),
      nextSeq,
    );
    setMeta(store, ACTIVE_KEY, promotion.promotionId);
  });
}

export function activeBuildId(
  store: SqlStore,
  app: string,
): string | undefined {
  const activeId = getMeta(store, ACTIVE_KEY);
  if (!activeId) return undefined;
  const row = store.sql
    .exec<{ builds: string }>(
      `SELECT builds FROM promotions WHERE promotion_id = ?`,
      activeId,
    )
    .toArray()[0];
  if (!row) return undefined;
  return (JSON.parse(row.builds) as Record<string, string>)[app];
}

export function history(store: SqlStore): HistoryEntry[] {
  const activeId = getMeta(store, ACTIVE_KEY);
  // Ordered newest-first by seq (promote assigns an increasing seq) per the
  // acceptance criteria, with the active promotion marked.
  return store.sql
    .exec<{ promotion_id: string; ts: number; builds: string }>(
      `SELECT promotion_id, ts, builds FROM promotions ORDER BY seq DESC`,
    )
    .toArray()
    .map((r) => ({
      promotionId: r.promotion_id,
      ts: r.ts,
      builds: JSON.parse(r.builds) as Record<string, string>,
      active: r.promotion_id === activeId,
    }));
}

export function prune(store: SqlStore, keepN: number): PruneResult {
  return store.transactionSync(() => {
    const activeId = getMeta(store, ACTIVE_KEY);
    const rows = store.sql
      .exec<{ promotion_id: string; builds: string }>(
        `SELECT promotion_id, builds FROM promotions ORDER BY seq DESC`,
      )
      .toArray();

    // The keep window is the N most recent promotions; the active one is
    // pinned even if it falls outside that window, so pruning can never take
    // the live site down.
    const kept: string[] = [];
    const removed: { promotionId: string; builds: Record<string, string> }[] =
      [];
    rows.forEach((r, i) => {
      if (i < keepN || r.promotion_id === activeId) {
        kept.push(r.promotion_id);
      } else {
        removed.push({
          promotionId: r.promotion_id,
          builds: JSON.parse(r.builds) as Record<string, string>,
        });
      }
    });

    const removedRecordKeys = removed.flatMap((p) =>
      Object.entries(p.builds).map(([app, buildId]) => recordKey(app, buildId)),
    );

    for (const p of removed) {
      store.sql.exec(
        `DELETE FROM promotions WHERE promotion_id = ?`,
        p.promotionId,
      );
      for (const [app, buildId] of Object.entries(p.builds)) {
        store.sql.exec(
          `DELETE FROM records WHERE app = ? AND build_id = ?`,
          app,
          buildId,
        );
      }
    }

    return {
      keptPromotionIds: kept,
      removedPromotionIds: removed.map((p) => p.promotionId),
      removedRecordKeys,
    };
  });
}

export function versionStamp(store: SqlStore): string | undefined {
  return getMeta(store, VERSION_KEY);
}

export function setVersionStamp(store: SqlStore, version: string): void {
  setMeta(store, VERSION_KEY, version);
}
