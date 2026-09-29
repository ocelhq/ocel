export interface SqlStore {
  sql: SqlStorage;
  transactionSync<T>(closure: () => T): T;
}

export interface DeploymentRecord {
  app: string;
  framework: string;
  identity: string;
  deploymentId: string;
  routingManifest: unknown;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  isrWriteSecret?: string;
  createdAt: number;
  edgeWorkers?: EdgeWorkers;
  buildFingerprint?: string;
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

export interface PointerMove {
  pointer?: string;
  replaces?: string | null;
  promotionId: string;
  records: DeploymentRecord[];
}

export type PointerMoveOutcome = "moved" | "stale";

export const SCHEMA_VERSION = 3;

const DEFAULT_POINTER = "@production";
const VERSION_KEY = "versionStamp";
const OWNER_KEY = "ownerToken";
const SECRET_KEY = "secret";
const SCHEMA_KEY = "schemaVersion";

function dropSupersededSchema(store: SqlStore): void {
  const recorded = store.sql
    .exec<{ value: string }>(`SELECT value FROM meta WHERE key = ?`, SCHEMA_KEY)
    .toArray()[0]?.value;
  if (recorded === String(SCHEMA_VERSION)) return;
  for (const table of ["records", "promotions", "pointers", "served", "apps"]) {
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
    `CREATE TABLE IF NOT EXISTS pointers (
       name TEXT PRIMARY KEY,
       promotion_id TEXT NOT NULL
     );
     CREATE TABLE IF NOT EXISTS served (
       pointer TEXT NOT NULL,
       app TEXT NOT NULL,
       identity TEXT NOT NULL,
       data TEXT NOT NULL,
       PRIMARY KEY (pointer, app)
     );
     CREATE TABLE IF NOT EXISTS apps (
       app TEXT PRIMARY KEY
     );`,
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

export function readServedPromotion(
  store: SqlStore,
  pointer: string = DEFAULT_POINTER,
): string | undefined {
  const row = store.sql
    .exec<{ promotion_id: string }>(`SELECT promotion_id FROM pointers WHERE name = ?`, pointer)
    .toArray()[0];
  return row?.promotion_id;
}

export function movePointer(store: SqlStore, move: PointerMove): PointerMoveOutcome {
  const pointer = move.pointer || DEFAULT_POINTER;
  return store.transactionSync(() => {
    if ((readServedPromotion(store, pointer) ?? null) !== (move.replaces || null)) return "stale";
    store.sql.exec(`DELETE FROM served WHERE pointer = ?`, pointer);
    for (const record of move.records) {
      store.sql.exec(
        `INSERT INTO served (pointer, app, identity, data) VALUES (?, ?, ?, ?)`,
        pointer,
        record.app,
        record.identity,
        JSON.stringify(record),
      );
      store.sql.exec(`INSERT OR IGNORE INTO apps (app) VALUES (?)`, record.app);
    }
    store.sql.exec(
      `INSERT INTO pointers (name, promotion_id) VALUES (?, ?)
       ON CONFLICT(name) DO UPDATE SET promotion_id = excluded.promotion_id`,
      pointer,
      move.promotionId,
    );
    return "moved";
  });
}

export function removePointer(store: SqlStore, pointer: string): void {
  store.transactionSync(() => {
    store.sql.exec(`DELETE FROM served WHERE pointer = ?`, pointer);
    store.sql.exec(`DELETE FROM pointers WHERE name = ?`, pointer);
  });
}

export function listApps(store: SqlStore): string[] {
  return store.sql
    .exec<{ app: string }>(`SELECT app FROM apps ORDER BY app`)
    .toArray()
    .map((r) => r.app);
}

export type PointerRecordResult =
  | { kind: "no-pointer" }
  | { kind: "ambiguous-app" }
  | { kind: "unchanged"; identity: string }
  | { kind: "record"; identity: string; record: DeploymentRecord };

export function pointerRecord(
  store: SqlStore,
  app?: string,
  pointer: string = DEFAULT_POINTER,
  knownIdentity?: string,
): PointerRecordResult {
  const rows = store.sql
    .exec<{ app: string; identity: string; data: string }>(
      `SELECT app, identity, data FROM served WHERE pointer = ? ORDER BY app`,
      pointer,
    )
    .toArray();
  if (app === undefined && rows.length > 1) return { kind: "ambiguous-app" };
  const row = app === undefined ? rows[0] : rows.find((r) => r.app === app);
  if (!row) return { kind: "no-pointer" };
  if (row.identity === knownIdentity) return { kind: "unchanged", identity: row.identity };
  return {
    kind: "record",
    identity: row.identity,
    record: JSON.parse(row.data) as DeploymentRecord,
  };
}

export type Initialization = "adopted" | "refused";

export function initialize(
  store: SqlStore,
  ownerToken: string,
  secret: string,
  force: boolean,
): Initialization {
  return store.transactionSync(() => {
    if (identityRecorded(store) && !force) return "refused";
    setMeta(store, OWNER_KEY, ownerToken);
    setMeta(store, SECRET_KEY, secret);
    return "adopted";
  });
}

function identityRecorded(store: SqlStore): boolean {
  return getMeta(store, OWNER_KEY) !== undefined && getMeta(store, SECRET_KEY) !== undefined;
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
