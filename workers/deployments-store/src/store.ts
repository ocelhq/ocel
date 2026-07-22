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
  // Optional immutable label stamped at deploy time, unique across a project's
  // live promotions. Absent when the promotion was deployed without one.
  tag?: string;
}

export interface HistoryEntry extends Promotion {
  active: boolean;
}

// Thrown by promote() when a deploy's tag is already held by a different live
// promotion. The host also fails such a deploy up front (before creating any
// infrastructure); this is the atomic backstop that closes the window between
// that check and promote, e.g. two concurrent deploys claiming the same tag.
export class TagConflictError extends Error {}

export interface PruneResult {
  keptPromotionIds: string[];
  removedPromotionIds: string[];
  // "record:<app>/<buildId>" keys whose records were deleted, so the caller
  // knows exactly which underlying artifacts (stacks, R2 assets, ISR entries)
  // it still needs to reclaim. Kept in this prefixed form because the Go host
  // (cloud/aws/deploy/prune.go ReclaimTargets) parses it verbatim.
  removedRecordKeys: string[];
}

// The reserved default pointer. The primary domain resolves it, and a promote
// that names no pointer moves it. Its leading "@" can never appear in a DNS
// label, so it can never collide with a preview pointer named after a subdomain
// slug. It is an implementation detail of this module: callers omit the pointer
// to address it, and never name it themselves.
const DEFAULT_POINTER = "@production";
const VERSION_KEY = "versionStamp";
// The instance's ownership keys: the self-minted owner token that distinguishes
// legitimate recovery from a slug collision, and the per-project secret every
// authenticated op is checked against (index.ts). Both live in the meta table,
// so a destroy() that clears storage takes them with it and frees the slug.
const OWNER_KEY = "ownerToken";
const SECRET_KEY = "secret";

// Thrown by initialize() when the instance is already owned by a different
// project (a slug collision). The account-level bootstrap credential can force
// past it to adopt an instance after total state loss; see index.ts.
export class OwnershipConflictError extends Error {}

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
       seq INTEGER NOT NULL,
       tag TEXT
     );
     CREATE TABLE IF NOT EXISTS pointers (
       name TEXT PRIMARY KEY,
       promotion_id TEXT NOT NULL
     );
     CREATE TABLE IF NOT EXISTS meta (
       key TEXT PRIMARY KEY,
       value TEXT NOT NULL
     );`,
  );

  // Migrate a promotions table created before tags existed: add the column,
  // then enforce tag uniqueness across live promotions (partial so the many
  // untagged rows never collide on NULL).
  const hasTag = store.sql
    .exec<{ name: string }>(`PRAGMA table_info(promotions)`)
    .toArray()
    .some((c) => c.name === "tag");
  if (!hasTag) {
    store.sql.exec(`ALTER TABLE promotions ADD COLUMN tag TEXT`);
  }
  store.sql.exec(
    `CREATE UNIQUE INDEX IF NOT EXISTS promotions_tag_unique
       ON promotions(tag) WHERE tag IS NOT NULL`,
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

// A pointer names the promotion a domain serves. There are arbitrarily many,
// keyed by name; the reserved DEFAULT_POINTER is the one the primary domain
// resolves, and preview pointers are named after their subdomain slug.
function getPointer(store: SqlStore, name: string): string | undefined {
  const row = store.sql
    .exec<{ promotion_id: string }>(
      `SELECT promotion_id FROM pointers WHERE name = ?`,
      name,
    )
    .toArray()[0];
  return row?.promotion_id;
}

function setPointer(store: SqlStore, name: string, promotionId: string): void {
  store.sql.exec(
    `INSERT INTO pointers (name, promotion_id) VALUES (?, ?)
     ON CONFLICT(name) DO UPDATE SET promotion_id = excluded.promotion_id`,
    name,
    promotionId,
  );
}

export function putStaged(store: SqlStore, record: DeploymentRecord): void {
  // Only ever writes the (app, build id) record. Pointers live in their own
  // table, so staging can never change what any domain is currently serving.
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

export function promote(
  store: SqlStore,
  promotion: Promotion,
  pointer: string = DEFAULT_POINTER,
): void {
  // Bumping seq to a fresh maximum on every promote — including a rollback's
  // re-promote of an existing id — is what makes the promoted entry the newest
  // in history(), so the previously-active promotion becomes the "immediately
  // previous" one that a further rollback rolls back to. The whole thing runs
  // in one transaction, so a crash mid-promote can never leave a pointer naming
  // a promotion that isn't in the table, or vice versa.
  store.transactionSync(() => {
    // A tag addresses exactly one promotion, so reject one already held by a
    // different promotion. A rollback re-promotes an existing id carrying its
    // own tag unchanged, so it never clashes with itself (promotion_id != ?).
    if (promotion.tag) {
      const clash = store.sql
        .exec<{ promotion_id: string }>(
          `SELECT promotion_id FROM promotions WHERE tag = ? AND promotion_id != ?`,
          promotion.tag,
          promotion.promotionId,
        )
        .toArray()[0];
      if (clash) {
        throw new TagConflictError(
          `tag "${promotion.tag}" is already used by promotion ${clash.promotion_id}`,
        );
      }
    }
    const nextSeq = store.sql
      .exec<{ n: number }>(`SELECT COALESCE(MAX(seq), 0) + 1 AS n FROM promotions`)
      .one().n;
    store.sql.exec(
      `INSERT INTO promotions (promotion_id, ts, builds, seq, tag) VALUES (?, ?, ?, ?, ?)
       ON CONFLICT(promotion_id) DO UPDATE SET
         ts = excluded.ts, builds = excluded.builds, seq = excluded.seq, tag = excluded.tag`,
      promotion.promotionId,
      promotion.ts,
      JSON.stringify(promotion.builds),
      nextSeq,
      promotion.tag ?? null,
    );
    setPointer(store, pointer, promotion.promotionId);
  });
}

export function pointerBuildId(
  store: SqlStore,
  app: string,
  pointer: string = DEFAULT_POINTER,
): string | undefined {
  const promotionId = getPointer(store, pointer);
  if (!promotionId) return undefined;
  const row = store.sql
    .exec<{ builds: string }>(
      `SELECT builds FROM promotions WHERE promotion_id = ?`,
      promotionId,
    )
    .toArray()[0];
  if (!row) return undefined;
  return (JSON.parse(row.builds) as Record<string, string>)[app];
}

// The Deployment resolution the frozen generic worker consumes for a given
// pointer, folding pointer read and record read into one call (ADR 0002).
// knownBuildId lets a caller that already holds a record skip re-transferring
// it: when the pointer's build still matches, the (potentially large) record is
// omitted and only the build id comes back.
//
// - no-pointer  the pointer names no promotion for the app (fresh project, or
//               an unknown preview).
// - unchanged   pointer build id equals knownBuildId; record deliberately omitted.
// - record      pointer build id differs (or knownBuildId absent); record included.
// - dangling    the pointer names a build the store holds no record for — an
//               invariant violation the caller surfaces rather than papering over.
export type PointerRecordResult =
  | { kind: "no-pointer" }
  | { kind: "unchanged"; buildId: string }
  | { kind: "record"; buildId: string; record: DeploymentRecord }
  | { kind: "dangling"; buildId: string };

export function pointerRecord(
  store: SqlStore,
  app: string,
  pointer: string = DEFAULT_POINTER,
  knownBuildId?: string,
): PointerRecordResult {
  const buildId = pointerBuildId(store, app, pointer);
  if (!buildId) return { kind: "no-pointer" };
  if (buildId === knownBuildId) return { kind: "unchanged", buildId };
  const rec = record(store, app, buildId);
  if (!rec) return { kind: "dangling", buildId };
  return { kind: "record", buildId, record: rec };
}

export function history(store: SqlStore): HistoryEntry[] {
  const activeId = getPointer(store, DEFAULT_POINTER);
  // Ordered newest-first by seq (promote assigns an increasing seq) per the
  // acceptance criteria, with the active promotion marked.
  return store.sql
    .exec<{ promotion_id: string; ts: number; builds: string; tag: string | null }>(
      `SELECT promotion_id, ts, builds, tag FROM promotions ORDER BY seq DESC`,
    )
    .toArray()
    .map((r) => {
      const entry: HistoryEntry = {
        promotionId: r.promotion_id,
        ts: r.ts,
        builds: JSON.parse(r.builds) as Record<string, string>,
        active: r.promotion_id === activeId,
      };
      if (r.tag) entry.tag = r.tag;
      return entry;
    });
}

export function prune(store: SqlStore, keepN: number): PruneResult {
  return store.transactionSync(() => {
    const activeId = getPointer(store, DEFAULT_POINTER);
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

// Seeds (or rotates) this instance's ownership. The account-level bootstrap
// credential authorizes the caller before this runs (index.ts); here we only
// enforce that one project can't silently take over another's instance:
// re-seeding with a matching owner token rotates the secret (recovery), a
// mismatch is a collision and is refused — unless force adopts the instance
// after total state loss, when only the human deploying can vouch for it.
export function initialize(
  store: SqlStore,
  ownerToken: string,
  secret: string,
  force: boolean,
): void {
  const existing = getMeta(store, OWNER_KEY);
  if (existing !== undefined && existing !== ownerToken && !force) {
    throw new OwnershipConflictError(
      "instance is already owned by a different project",
    );
  }
  store.transactionSync(() => {
    setMeta(store, OWNER_KEY, ownerToken);
    setMeta(store, SECRET_KEY, secret);
  });
}

// The instance's stored project secret, or undefined before it is initialized.
// index.ts constant-time-compares an incoming bearer token against this to
// authenticate every non-initialize op.
export function storedSecret(store: SqlStore): string | undefined {
  return getMeta(store, SECRET_KEY);
}

export function versionStamp(store: SqlStore): string | undefined {
  return getMeta(store, VERSION_KEY);
}

export function setVersionStamp(store: SqlStore, version: string): void {
  setMeta(store, VERSION_KEY, version);
}
