import type { ReleaseRecord } from "@platform/edge-contract/release-record";
import {
  parseStorageCoordinate,
  type StorageCoordinate,
} from "@platform/edge-contract/storage-key";
import { matchesSecret } from "./auth";

export interface SqlStore {
  sql: SqlStorage;
  transactionSync<T>(closure: () => T): T;
}

export interface StoredRecord extends ReleaseRecord {
  edge?: unknown;
  buildCoordinates?: Record<string, string>;
}

export interface PointerMove {
  pointer?: string;
  replaces?: string | null;
  promotionId: string;
  records: StoredRecord[];
  labels?: ServedLabel[];
  kept: KeptRelease[];
}

export interface KeptRelease {
  app: string;
  releaseToken: string;
}

export interface ServedLabel {
  label: string;
  app: string;
}

export type PointerMoveOutcome =
  | { kind: "moved" }
  | { kind: "stale" }
  | { kind: "different-record"; app: string; release: string }
  | { kind: "unlocated"; app: string; release: string };

const DEFAULT_POINTER = "@production";
const VERSION_KEY = "versionStamp";
const GATE_KEY = "gate";
const SECRET_KEY = "secret";

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
       storage_coordinate TEXT NOT NULL,
       release_token TEXT NOT NULL,
       seq INTEGER NOT NULL,
       PRIMARY KEY (pointer, app, build_id)
     );
     CREATE TABLE IF NOT EXISTS kept (
       pointer TEXT NOT NULL,
       app TEXT NOT NULL,
       release_token TEXT NOT NULL,
       PRIMARY KEY (pointer, app, release_token)
     );
     CREATE INDEX IF NOT EXISTS kept_release ON kept (app, release_token);`,
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
  const located: { record: StoredRecord; storage: StorageCoordinate }[] = [];
  for (const record of move.records) {
    const storage = parseStorageCoordinate(record.storageCoordinate);
    if (storage === undefined)
      return { kind: "unlocated", app: record.app, release: record.release };
    located.push({ record, storage });
  }
  return store.transactionSync(() => {
    if ((readServedPromotion(store, pointer) ?? null) !== (move.replaces || null)) {
      return { kind: "stale" };
    }
    const changed = move.records.find((record) => servesDifferentCore(store, record));
    if (changed) return { kind: "different-record", app: changed.app, release: changed.release };
    store.sql.exec(`DELETE FROM kept WHERE pointer = ?`, pointer);
    for (const kept of move.kept) {
      store.sql.exec(
        `INSERT OR IGNORE INTO kept (pointer, app, release_token) VALUES (?, ?, ?)`,
        pointer,
        kept.app,
        kept.releaseToken,
      );
    }
    store.sql.exec(`DELETE FROM served WHERE pointer = ?`, pointer);
    for (const { record, storage } of located) {
      store.sql.exec(
        `INSERT INTO served (pointer, app, release, data) VALUES (?, ?, ?, ?)`,
        pointer,
        record.app,
        record.release,
        JSON.stringify(withBuildCoordinates(record, recordBuild(store, pointer, record, storage))),
      );
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
    return { kind: "moved" };
  });
}

function coreJson(record: StoredRecord): string {
  const { edge: _edge, buildCoordinates: _buildCoordinates, ...core } = record;
  return JSON.stringify(core, (_key, value: unknown) =>
    value !== null && typeof value === "object" && !Array.isArray(value)
      ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)))
      : value,
  );
}

function servesDifferentCore(store: SqlStore, record: StoredRecord): boolean {
  const row = store.sql
    .exec<{ data: string }>(
      `SELECT data FROM served WHERE app = ? AND release = ? LIMIT 1`,
      record.app,
      record.release,
    )
    .toArray()[0];
  return row !== undefined && coreJson(JSON.parse(row.data) as StoredRecord) !== coreJson(record);
}

function recordBuild(
  store: SqlStore,
  pointer: string,
  record: StoredRecord,
  storage: StorageCoordinate,
): Record<string, string> {
  store.sql.exec(
    `INSERT INTO builds (pointer, app, build_id, storage_coordinate, release_token, seq)
     VALUES (?, ?, ?, ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM builds))
     ON CONFLICT(pointer, app, build_id) DO UPDATE SET
       storage_coordinate = excluded.storage_coordinate, release_token = excluded.release_token,
       seq = excluded.seq`,
    pointer,
    record.app,
    record.buildId,
    record.storageCoordinate,
    storage.release,
  );
  store.sql.exec(
    `DELETE FROM builds WHERE pointer = ? AND app = ? AND NOT EXISTS (
       SELECT 1 FROM kept WHERE kept.pointer = builds.pointer AND kept.app = builds.app
         AND kept.release_token = builds.release_token
     )`,
    pointer,
    record.app,
  );
  const rows = store.sql
    .exec<{ build_id: string; storage_coordinate: string }>(
      `SELECT build_id, storage_coordinate FROM builds
       WHERE pointer = ? AND app = ? AND build_id != ? ORDER BY seq`,
      pointer,
      record.app,
      record.buildId,
    )
    .toArray();
  return Object.fromEntries(rows.map((row) => [row.build_id, row.storage_coordinate]));
}

function withBuildCoordinates(
  record: StoredRecord,
  buildCoordinates: Record<string, string>,
): StoredRecord {
  const { buildCoordinates: _, ...rest } = record;
  return Object.keys(buildCoordinates).length === 0 ? rest : { ...rest, buildCoordinates };
}

export function removePointer(store: SqlStore, pointer: string): void {
  store.transactionSync(() => {
    store.sql.exec(`DELETE FROM builds WHERE pointer = ?`, pointer);
    store.sql.exec(`DELETE FROM served WHERE pointer = ?`, pointer);
    store.sql.exec(`DELETE FROM pointers WHERE name = ?`, pointer);
    store.sql.exec(`DELETE FROM labels WHERE pointer = ?`, pointer);
    store.sql.exec(`DELETE FROM kept WHERE pointer = ?`, pointer);
  });
}

export type PointerRecordResult<Record = StoredRecord> =
  | { kind: "no-pointer" }
  | { kind: "ambiguous-app" }
  | { kind: "unchanged"; release: string }
  | { kind: "record"; release: string; record: Record };

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
    record: JSON.parse(row.data) as StoredRecord,
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

export function listOtherAppReleases(store: SqlStore, storageCoordinate: string): string[] {
  const coordinate = parseStorageCoordinate(storageCoordinate);
  if (coordinate === undefined) return [];
  const app = `${coordinate.project}/${coordinate.env}/${coordinate.app}/`;
  return store.sql
    .exec<{ storage_coordinate: string }>(
      `SELECT storage_coordinate FROM (
         SELECT storage_coordinate FROM builds
         UNION SELECT json_extract(data, '$.storageCoordinate') FROM served
       ) WHERE substr(storage_coordinate, 1, ?) = ? AND storage_coordinate != ?`,
      app.length,
      app,
      storageCoordinate,
    )
    .toArray()
    .map((row) => row.storage_coordinate);
}

export type ServedRecordResult<Record = StoredRecord> =
  | { kind: "record"; record: Record }
  | { kind: "not-served" }
  | { kind: "forgotten" };

export function readServedRecord(store: SqlStore, storageCoordinate: string): ServedRecordResult {
  const row = store.sql
    .exec<{ data: string }>(
      `SELECT data FROM served WHERE json_extract(data, '$.storageCoordinate') = ? LIMIT 1`,
      storageCoordinate,
    )
    .toArray()[0];
  if (row) return { kind: "record", record: JSON.parse(row.data) as StoredRecord };
  const coordinate = parseStorageCoordinate(storageCoordinate);
  if (coordinate === undefined) return { kind: "not-served" };
  const kept = store.sql
    .exec(
      `SELECT 1 FROM kept WHERE app = ? AND release_token = ? LIMIT 1`,
      coordinate.app,
      coordinate.release,
    )
    .toArray();
  return kept.length > 0 ? { kind: "not-served" } : { kind: "forgotten" };
}

export type Initialization = "adopted" | "refused";

export async function initialize(store: SqlStore, secret: string): Promise<Initialization> {
  ensureSchema(store);
  const held = store.transactionSync(() => {
    const existing = getMeta(store, SECRET_KEY);
    if (existing === undefined) setMeta(store, SECRET_KEY, secret);
    return existing;
  });
  if (held === undefined) return "adopted";
  return (await matchesSecret(secret, held)) ? "adopted" : "refused";
}

export function readSecret(store: SqlStore): string | undefined {
  const meta = store.sql
    .exec(`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'meta'`)
    .toArray();
  return meta.length === 0 ? undefined : getMeta(store, SECRET_KEY);
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
