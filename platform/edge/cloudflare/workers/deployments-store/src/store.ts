export interface SqlStore {
  sql: SqlStorage;
  transactionSync<T>(closure: () => T): T;
}

export interface DeploymentRecord {
  app: string;
  framework: string;
  identity: string;
  deploymentId: string;
  buildId: string;
  routingManifest: unknown;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  isrWriteSecret?: string;
  createdAt: number;
  edgeWorkers?: EdgeWorkers;
  valueFingerprint?: string;
  variables?: VariableRecord[];
}

export interface VariableRecord {
  key: string;
  folder?: string;
  version?: number;
  live?: boolean;
}

export interface EdgeWorkers {
  bundleKey: string;
  id: string;
  compatDate: string;
  compatFlags?: string[];
}

export interface Promotion {
  promotionId: string;
  ts: number;
  builds: Record<string, string>;
  tag?: string;
}

export interface HistoryEntry extends Promotion {
  active: boolean;
}

export class TagConflictError extends Error {}

export interface PruneResult {
  keptPromotionIds: string[];
  removedPromotionIds: string[];
  removedRecordKeys: string[];
  survivingRecordKeys: string[];
  survivingPointerRecordKeys: string[];
}

export const SCHEMA_VERSION = 2;

const DEFAULT_POINTER = "@production";
const VERSION_KEY = "versionStamp";
const OWNER_KEY = "ownerToken";
const SECRET_KEY = "secret";
const SCHEMA_KEY = "schemaVersion";

function recordKey(app: string, identity: string): string {
  return `record:${app}/${identity}`;
}

function dropSupersededSchema(store: SqlStore): void {
  const recorded = store.sql
    .exec<{ value: string }>(`SELECT value FROM meta WHERE key = ?`, SCHEMA_KEY)
    .toArray()[0]?.value;
  if (recorded === String(SCHEMA_VERSION)) return;
  for (const table of ["records", "promotions", "pointers"]) {
    store.sql.exec(`DROP TABLE IF EXISTS ${table}`);
  }
}

export function ensureSchema(store: SqlStore): void {
  store.sql.exec(
    `CREATE TABLE IF NOT EXISTS meta (
       key TEXT PRIMARY KEY,
       value TEXT NOT NULL
     );`,
  );
  dropSupersededSchema(store);
  store.sql.exec(
    `CREATE TABLE IF NOT EXISTS records (
       app TEXT NOT NULL,
       identity TEXT NOT NULL,
       data TEXT NOT NULL,
       PRIMARY KEY (app, identity)
     );
     CREATE TABLE IF NOT EXISTS promotions (
       promotion_id TEXT PRIMARY KEY,
       ts INTEGER NOT NULL,
       builds TEXT NOT NULL,
       seq INTEGER NOT NULL,
       tag TEXT,
       pointer TEXT NOT NULL DEFAULT '${DEFAULT_POINTER}'
     );
     CREATE TABLE IF NOT EXISTS pointers (
       name TEXT PRIMARY KEY,
       promotion_id TEXT NOT NULL
     );`,
  );
  store.sql.exec(
    `CREATE UNIQUE INDEX IF NOT EXISTS promotions_tag_unique
       ON promotions(tag) WHERE tag IS NOT NULL`,
  );
  setMeta(store, SCHEMA_KEY, String(SCHEMA_VERSION));
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
  store.sql.exec(
    `INSERT INTO records (app, identity, data) VALUES (?, ?, ?)
     ON CONFLICT(app, identity) DO UPDATE SET data = excluded.data`,
    record.app,
    record.identity,
    JSON.stringify(record),
  );
}

export function record(
  store: SqlStore,
  app: string,
  identity: string,
): DeploymentRecord | undefined {
  const row = store.sql
    .exec<{ data: string }>(
      `SELECT data FROM records WHERE app = ? AND identity = ?`,
      app,
      identity,
    )
    .toArray()[0];
  return row ? (JSON.parse(row.data) as DeploymentRecord) : undefined;
}

export function promote(
  store: SqlStore,
  promotion: Promotion,
  pointer: string = DEFAULT_POINTER,
): void {
  store.transactionSync(() => {
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
      `INSERT INTO promotions (promotion_id, ts, builds, seq, tag, pointer) VALUES (?, ?, ?, ?, ?, ?)
       ON CONFLICT(promotion_id) DO UPDATE SET
         ts = excluded.ts, builds = excluded.builds, seq = excluded.seq, tag = excluded.tag, pointer = excluded.pointer`,
      promotion.promotionId,
      promotion.ts,
      JSON.stringify(promotion.builds),
      nextSeq,
      promotion.tag ?? null,
      pointer,
    );
    setPointer(store, pointer, promotion.promotionId);
  });
}

function pointerBuilds(
  store: SqlStore,
  pointer: string,
): Record<string, string> | undefined {
  const promotionId = getPointer(store, pointer);
  if (!promotionId) return undefined;
  const row = store.sql
    .exec<{ builds: string }>(
      `SELECT builds FROM promotions WHERE promotion_id = ?`,
      promotionId,
    )
    .toArray()[0];
  if (!row) return undefined;
  return JSON.parse(row.builds) as Record<string, string>;
}

export function pointerIdentity(
  store: SqlStore,
  app: string,
  pointer: string = DEFAULT_POINTER,
): string | undefined {
  return pointerBuilds(store, pointer)?.[app];
}

export type PointerRecordResult =
  | { kind: "no-pointer" }
  | { kind: "ambiguous-app" }
  | { kind: "unchanged"; identity: string }
  | { kind: "record"; identity: string; record: DeploymentRecord }
  | { kind: "dangling"; identity: string };

export function pointerRecord(
  store: SqlStore,
  app?: string,
  pointer: string = DEFAULT_POINTER,
  knownIdentity?: string,
): PointerRecordResult {
  const builds = pointerBuilds(store, pointer);
  if (!builds) return { kind: "no-pointer" };

  let target = app;
  if (target === undefined) {
    const apps = Object.keys(builds);
    if (apps.length > 1) return { kind: "ambiguous-app" };
    if (apps.length === 0) return { kind: "no-pointer" };
    target = apps[0];
  }

  const identity = builds[target];
  if (!identity) return { kind: "no-pointer" };
  if (identity === knownIdentity) return { kind: "unchanged", identity };
  const rec = record(store, target, identity);
  if (!rec) return { kind: "dangling", identity };
  return { kind: "record", identity, record: rec };
}

export function history(
  store: SqlStore,
  pointer: string = DEFAULT_POINTER,
): HistoryEntry[] {
  const activeId = getPointer(store, pointer);
  return store.sql
    .exec<{ promotion_id: string; ts: number; builds: string; tag: string | null }>(
      `SELECT promotion_id, ts, builds, tag FROM promotions WHERE pointer = ? ORDER BY seq DESC`,
      pointer,
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

export function prune(
  store: SqlStore,
  keepN: number,
  pointer: string = DEFAULT_POINTER,
): PruneResult {
  return store.transactionSync(() => {
    const activeId = getPointer(store, pointer);
    const rows = store.sql
      .exec<{ promotion_id: string; builds: string }>(
        `SELECT promotion_id, builds FROM promotions WHERE pointer = ? ORDER BY seq DESC`,
        pointer,
      )
      .toArray();

    const kept: { promotionId: string; builds: Record<string, string> }[] = [];
    const removed: { promotionId: string; builds: Record<string, string> }[] =
      [];
    rows.forEach((r, i) => {
      if (i < keepN || r.promotion_id === activeId) {
        kept.push({
          promotionId: r.promotion_id,
          builds: JSON.parse(r.builds) as Record<string, string>,
        });
      } else {
        removed.push({
          promotionId: r.promotion_id,
          builds: JSON.parse(r.builds) as Record<string, string>,
        });
      }
    });

    const removedRecordKeys = removed.flatMap((p) =>
      Object.entries(p.builds).map(([app, identity]) => recordKey(app, identity)),
    );

    for (const p of removed) {
      store.sql.exec(
        `DELETE FROM promotions WHERE promotion_id = ?`,
        p.promotionId,
      );
      for (const [app, identity] of Object.entries(p.builds)) {
        store.sql.exec(
          `DELETE FROM records WHERE app = ? AND identity = ?`,
          app,
          identity,
        );
      }
    }

    return {
      keptPromotionIds: kept.map((p) => p.promotionId),
      removedPromotionIds: removed.map((p) => p.promotionId),
      removedRecordKeys,
      survivingRecordKeys: remainingRecordKeys(store),
      survivingPointerRecordKeys: promotedRecordKeys(kept),
    };
  });
}

function promotedRecordKeys(
  promotions: { builds: Record<string, string> }[],
): string[] {
  const keys = new Set<string>();
  for (const p of promotions) {
    for (const [app, identity] of Object.entries(p.builds)) {
      keys.add(recordKey(app, identity));
    }
  }
  return [...keys].sort();
}

export function removePointer(
  store: SqlStore,
  pointer: string = DEFAULT_POINTER,
): PruneResult {
  return store.transactionSync(() => {
    const rows = store.sql
      .exec<{ promotion_id: string; builds: string }>(
        `SELECT promotion_id, builds FROM promotions WHERE pointer = ? ORDER BY seq DESC`,
        pointer,
      )
      .toArray();

    const removed = rows.map((r) => ({
      promotionId: r.promotion_id,
      builds: JSON.parse(r.builds) as Record<string, string>,
    }));
    const removedRecordKeys = removed.flatMap((p) =>
      Object.entries(p.builds).map(([app, identity]) => recordKey(app, identity)),
    );

    for (const p of removed) {
      store.sql.exec(`DELETE FROM promotions WHERE promotion_id = ?`, p.promotionId);
      for (const [app, identity] of Object.entries(p.builds)) {
        store.sql.exec(
          `DELETE FROM records WHERE app = ? AND identity = ?`,
          app,
          identity,
        );
      }
    }
    store.sql.exec(`DELETE FROM pointers WHERE name = ?`, pointer);

    return {
      keptPromotionIds: [],
      removedPromotionIds: removed.map((p) => p.promotionId),
      removedRecordKeys,
      survivingRecordKeys: remainingRecordKeys(store),
      survivingPointerRecordKeys: [],
    };
  });
}

function remainingRecordKeys(store: SqlStore): string[] {
  return store.sql
    .exec<{ app: string; identity: string }>(
      `SELECT app, identity FROM records ORDER BY app, identity`,
    )
    .toArray()
    .map((r) => recordKey(r.app, r.identity));
}

export interface Identity {
  ownerToken: string;
  secret: string;
}

export function initialize(
  store: SqlStore,
  ownerToken: string,
  secret: string,
  force: boolean,
): Identity {
  return store.transactionSync(() => {
    const existing = storedIdentity(store);
    if (existing && !force) return existing;
    setMeta(store, OWNER_KEY, ownerToken);
    setMeta(store, SECRET_KEY, secret);
    return { ownerToken, secret };
  });
}

function storedIdentity(store: SqlStore): Identity | undefined {
  const ownerToken = getMeta(store, OWNER_KEY);
  const secret = getMeta(store, SECRET_KEY);
  return ownerToken !== undefined && secret !== undefined
    ? { ownerToken, secret }
    : undefined;
}

export function storedSecret(store: SqlStore): string | undefined {
  return getMeta(store, SECRET_KEY);
}

export function versionStamp(store: SqlStore): string | undefined {
  return getMeta(store, VERSION_KEY);
}

export function setVersionStamp(store: SqlStore, version: string): void {
  setMeta(store, VERSION_KEY, version);
}
