export interface SqlStore {
  sql: SqlStorage;
  transactionSync<T>(closure: () => T): T;
}

export interface ReleaseRecord {
  app: string;
  framework: string;
  release: string;
  buildId: string;
  routeTable?: unknown;
  functionUrls: Record<string, string>;
  assetPrefix: string;
  isrPrefix: string;
  purgeSecret?: string;
  createdAt: number;
  edgeWorkers?: EdgeWorkers;
  releaseFingerprint?: string;
  variables?: VariableRecord[];
  buildAssets?: Record<string, string>;
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

const DEFAULT_POINTER = "@production";
const VERSION_KEY = "versionStamp";
const GATE_KEY = "gate";
const SECRET_KEY = "secret";

export const KEPT_BUILDS_PER_APP = 20;

export function ensureSchema(store: SqlStore): void {
  store.sql.exec(
    `CREATE TABLE IF NOT EXISTS meta (
       key TEXT PRIMARY KEY,
       value TEXT NOT NULL
     );`,
  );
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
     CREATE INDEX IF NOT EXISTS labels_pointer ON labels (pointer);
     CREATE TABLE IF NOT EXISTS builds (
       pointer TEXT NOT NULL,
       app TEXT NOT NULL,
       build_id TEXT NOT NULL,
       asset_prefix TEXT NOT NULL,
       seq INTEGER NOT NULL,
       PRIMARY KEY (pointer, app, build_id)
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
        JSON.stringify(withBuildAssets(record, recordBuild(store, pointer, record))),
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

function recordBuild(
  store: SqlStore,
  pointer: string,
  record: ReleaseRecord,
): Record<string, string> {
  store.sql.exec(
    `INSERT INTO builds (pointer, app, build_id, asset_prefix, seq)
     VALUES (?, ?, ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM builds))
     ON CONFLICT(pointer, app, build_id) DO UPDATE SET
       asset_prefix = excluded.asset_prefix, seq = excluded.seq`,
    pointer,
    record.app,
    record.buildId,
    record.assetPrefix,
  );
  store.sql.exec(
    `DELETE FROM builds WHERE pointer = ? AND app = ? AND build_id NOT IN (
       SELECT build_id FROM builds WHERE pointer = ? AND app = ? ORDER BY seq DESC LIMIT ?
     )`,
    pointer,
    record.app,
    pointer,
    record.app,
    KEPT_BUILDS_PER_APP,
  );
  const rows = store.sql
    .exec<{ build_id: string; asset_prefix: string }>(
      `SELECT build_id, asset_prefix FROM builds
       WHERE pointer = ? AND app = ? AND build_id != ? ORDER BY seq`,
      pointer,
      record.app,
      record.buildId,
    )
    .toArray();
  return Object.fromEntries(rows.map((row) => [row.build_id, row.asset_prefix]));
}

function withBuildAssets(
  record: ReleaseRecord,
  buildAssets: Record<string, string>,
): ReleaseRecord {
  const { buildAssets: _, ...rest } = record;
  return Object.keys(buildAssets).length === 0 ? rest : { ...rest, buildAssets };
}

export function removePointer(store: SqlStore, pointer: string): void {
  store.transactionSync(() => {
    store.sql.exec(`DELETE FROM builds WHERE pointer = ?`, pointer);
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

export type ServedRecordResult = { kind: "record"; record: ReleaseRecord } | { kind: "not-served" };

export function readServedRecord(store: SqlStore, isrPrefix: string): ServedRecordResult {
  const row = store.sql
    .exec<{ data: string }>(
      `SELECT data FROM served WHERE json_extract(data, '$.isrPrefix') = ? LIMIT 1`,
      isrPrefix,
    )
    .toArray()[0];
  if (!row) return { kind: "not-served" };
  return { kind: "record", record: JSON.parse(row.data) as ReleaseRecord };
}

export type Initialization = "adopted" | "refused";

export function initialize(store: SqlStore, secret: string): Initialization {
  return store.transactionSync(() => {
    if (readSecret(store) !== undefined) return "refused";
    setMeta(store, SECRET_KEY, secret);
    return "adopted";
  });
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

export function readGate(store: SqlStore): string | undefined {
  return getMeta(store, GATE_KEY);
}

export function setGate(store: SqlStore, gate: string | undefined): void {
  if (gate === undefined) {
    store.sql.exec(`DELETE FROM meta WHERE key = ?`, GATE_KEY);
    return;
  }
  setMeta(store, GATE_KEY, gate);
}
