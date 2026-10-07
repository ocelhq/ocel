export interface SqlStore {
  sql: SqlStorage;
  transactionSync<T>(closure: () => T): T;
}

export interface ReleaseRecord {
  app: string;
  framework: string;
  release: string;
  buildId: string;
  routingManifest: unknown;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  isrWriteSecret?: string;
  createdAt: number;
  edgeWorkers?: EdgeWorkers;
  releaseFingerprint?: string;
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
  records: ReleaseRecord[];
  labels?: ServedLabel[];
}

export interface ServedLabel {
  label: string;
  app: string;
}

export type PointerMoveOutcome = "moved" | "stale";

export const SCHEMA_VERSION = 4;

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
  for (const table of ["records", "promotions", "pointers", "served", "apps", "labels"]) {
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
       release TEXT NOT NULL,
       data TEXT NOT NULL,
       PRIMARY KEY (pointer, app)
     );
     CREATE TABLE IF NOT EXISTS apps (
       app TEXT PRIMARY KEY
     );
     CREATE TABLE IF NOT EXISTS labels (
       label TEXT PRIMARY KEY,
       pointer TEXT NOT NULL,
       app TEXT NOT NULL
     );
     CREATE INDEX IF NOT EXISTS labels_pointer ON labels (pointer);`,
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
        `INSERT INTO served (pointer, app, release, data) VALUES (?, ?, ?, ?)`,
        pointer,
        record.app,
        record.release,
        JSON.stringify(record),
      );
      store.sql.exec(`INSERT OR IGNORE INTO apps (app) VALUES (?)`, record.app);
    }
    store.sql.exec(`DELETE FROM labels WHERE pointer = ?`, pointer);
    for (const served of move.labels ?? []) {
      store.sql.exec(
        `INSERT INTO labels (label, pointer, app) VALUES (?, ?, ?)
         ON CONFLICT(label) DO UPDATE SET pointer = excluded.pointer, app = excluded.app`,
        served.label,
        pointer,
        served.app,
      );
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
    store.sql.exec(`DELETE FROM labels WHERE pointer = ?`, pointer);
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
  | { kind: "unchanged"; release: string }
  | { kind: "record"; release: string; record: ReleaseRecord };

type ServedRow = { app: string; release: string; data: string };

function selectServedResult(
  rows: ServedRow[],
  app: string | undefined,
  knownRelease: string | undefined,
): PointerRecordResult {
  if (app === undefined && rows.length > 1) return { kind: "ambiguous-app" };
  const row = app === undefined ? rows[0] : rows.find((r) => r.app === app);
  if (!row) return { kind: "no-pointer" };
  if (row.release === knownRelease) return { kind: "unchanged", release: row.release };
  return {
    kind: "record",
    release: row.release,
    record: JSON.parse(row.data) as ReleaseRecord,
  };
}

export function readLabelRecord(
  store: SqlStore,
  label: string,
  knownRelease?: string,
): PointerRecordResult {
  const rows = store.sql
    .exec<ServedRow>(
      `SELECT served.app, served.release, served.data
       FROM labels JOIN served ON served.pointer = labels.pointer
         AND (labels.app = '' OR served.app = labels.app)
       WHERE labels.label = ?
       ORDER BY served.app`,
      label,
    )
    .toArray();
  return selectServedResult(rows, undefined, knownRelease);
}

export function readPointerRecord(
  store: SqlStore,
  app?: string,
  knownRelease?: string,
): PointerRecordResult {
  const rows = store.sql
    .exec<ServedRow>(
      `SELECT app, release, data FROM served WHERE pointer = ? ORDER BY app`,
      DEFAULT_POINTER,
    )
    .toArray();
  return selectServedResult(rows, app, knownRelease);
}

export type Initialization = "adopted" | "refused";

export function initialize(
  store: SqlStore,
  ownerToken: string,
  secret: string,
  force: boolean,
): Initialization {
  return store.transactionSync(() => {
    if (isIdentityRecorded(store) && !force) return "refused";
    setMeta(store, OWNER_KEY, ownerToken);
    setMeta(store, SECRET_KEY, secret);
    return "adopted";
  });
}

function isIdentityRecorded(store: SqlStore): boolean {
  return getMeta(store, OWNER_KEY) !== undefined && getMeta(store, SECRET_KEY) !== undefined;
}

export function readSecret(store: SqlStore): string | undefined {
  return getMeta(store, SECRET_KEY);
}

export function readVersionStamp(store: SqlStore): string | undefined {
  return getMeta(store, VERSION_KEY);
}

export function setVersionStamp(store: SqlStore, version: string): void {
  setMeta(store, VERSION_KEY, version);
}
