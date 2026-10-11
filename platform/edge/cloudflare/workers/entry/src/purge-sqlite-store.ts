import type { PurgeMode } from "@platform/edge-contract/purge";
import type { BacklogStore } from "./purge-backlog";

const EVERYTHING_AT = "purgedEverythingAt";
const RETRY_AT = "retryAt";
const FAILURES = "failures";

export function sqliteBacklogStore(storage: DurableObjectStorage): BacklogStore {
  const { sql } = storage;
  sql.exec(
    `CREATE TABLE IF NOT EXISTS pending (
       tag TEXT PRIMARY KEY,
       mode TEXT NOT NULL,
       raised_at INTEGER NOT NULL,
       latest_at INTEGER NOT NULL
     );
     CREATE INDEX IF NOT EXISTS pending_by_mode ON pending (mode, raised_at);
     CREATE TABLE IF NOT EXISTS purged (
       tag TEXT PRIMARY KEY,
       purged_at INTEGER,
       invalidated_at INTEGER
     );
     CREATE TABLE IF NOT EXISTS meta (
       key TEXT PRIMARY KEY,
       value INTEGER NOT NULL
     );`,
  );

  const readNumber = (query: string, ...bindings: (string | number)[]): number | undefined => {
    const row = sql.exec<{ value: number | null }>(query, ...bindings).toArray()[0];
    return row?.value ?? undefined;
  };
  const readMeta = (key: string) => readNumber(`SELECT value FROM meta WHERE key = ?`, key);
  const writeMeta = (key: string, value: number) =>
    sql.exec(
      `INSERT INTO meta (key, value) VALUES (?, ?)
       ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
      key,
      value,
    );
  const deleteMeta = (key: string) => sql.exec(`DELETE FROM meta WHERE key = ?`, key);

  return {
    readCoveredAt(tag, mode) {
      return readNumber(
        mode === "purge"
          ? `SELECT purged_at AS value FROM purged WHERE tag = ?`
          : `SELECT MAX(COALESCE(purged_at, 0), COALESCE(invalidated_at, 0)) AS value
             FROM purged WHERE tag = ?`,
        tag,
      );
    },
    readPurgedEverythingAt: () => readMeta(EVERYTHING_AT),
    readRetryAt: () => readMeta(RETRY_AT),
    readFailures: () => readMeta(FAILURES) ?? 0,
    readOldestRaisedAt: () => readNumber(`SELECT MIN(raised_at) AS value FROM pending`),
    hasPending: (mode) =>
      sql.exec(`SELECT 1 FROM pending WHERE mode = ? LIMIT 1`, mode).toArray().length > 0,
    hasAnyPending: () => sql.exec(`SELECT 1 FROM pending LIMIT 1`).toArray().length > 0,
    readBatch: (mode, limit) =>
      sql
        .exec<{ tag: string }>(
          `SELECT tag FROM pending WHERE mode = ? ORDER BY raised_at, tag LIMIT ?`,
          mode,
          limit,
        )
        .toArray()
        .map((row) => row.tag),
    addPending(mode, tag, raisedAt) {
      sql.exec(
        `INSERT INTO pending (tag, mode, raised_at, latest_at) VALUES (?, ?, ?, ?)
         ON CONFLICT(tag) DO UPDATE SET
           mode = CASE WHEN pending.mode = 'purge' OR excluded.mode = 'purge'
                       THEN 'purge' ELSE 'invalidate' END,
           raised_at = MIN(pending.raised_at, excluded.raised_at),
           latest_at = MAX(pending.latest_at, excluded.latest_at)`,
        tag,
        mode,
        raisedAt,
        raisedAt,
      );
    },
    settleBatch(mode: PurgeMode, tags, startedAt) {
      const column = mode === "purge" ? "purged_at" : "invalidated_at";
      storage.transactionSync(() => {
        for (const tag of tags) {
          sql.exec(
            `INSERT INTO purged (tag, ${column}) VALUES (?, ?)
             ON CONFLICT(tag) DO UPDATE SET ${column} = MAX(COALESCE(purged.${column}, 0), excluded.${column})`,
            tag,
            startedAt,
          );
          sql.exec(
            `DELETE FROM pending WHERE tag = ? AND mode = ? AND latest_at <= ?`,
            tag,
            mode,
            startedAt,
          );
          sql.exec(
            `UPDATE pending SET raised_at = MAX(raised_at, ?) WHERE tag = ? AND mode = ?`,
            startedAt,
            tag,
            mode,
          );
        }
      });
    },
    settleEverything(startedAt) {
      storage.transactionSync(() => {
        writeMeta(EVERYTHING_AT, startedAt);
        sql.exec(`DELETE FROM pending WHERE latest_at <= ?`, startedAt);
        sql.exec(`UPDATE pending SET raised_at = MAX(raised_at, ?)`, startedAt);
      });
    },
    writeBackoff(failures, retryAt) {
      storage.transactionSync(() => {
        writeMeta(FAILURES, failures);
        writeMeta(RETRY_AT, retryAt);
      });
    },
    clearBackoff() {
      storage.transactionSync(() => {
        deleteMeta(FAILURES);
        deleteMeta(RETRY_AT);
      });
    },
    forgetCoveredBefore(at) {
      sql.exec(
        `DELETE FROM purged
         WHERE MAX(COALESCE(purged_at, 0), COALESCE(invalidated_at, 0)) < ?`,
        at,
      );
    },
  };
}
