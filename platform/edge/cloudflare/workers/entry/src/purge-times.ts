import type { PurgeMode } from "@platform/edge-contract/purge";
import type { RaisedTag } from "./purge-backlog";

export const TAG_TIMES_RETENTION_MS = 86_400_000;

export interface TagTime {
  purgedAt?: number;
  invalidatedAt?: number;
  expiresAt?: number;
}

export type TagTimes = Record<string, TagTime>;

export interface TagTimesStore {
  record(mode: PurgeMode, tags: readonly RaisedTag[]): void;
  read(tags: readonly string[]): TagTimes;
  forgetBefore(at: number): void;
}

const TAGS_PER_QUERY = 100;

export function newSqliteTagTimes(storage: DurableObjectStorage): TagTimesStore {
  const { sql } = storage;
  sql.exec(
    `CREATE TABLE IF NOT EXISTS tag_times (
       tag TEXT PRIMARY KEY,
       purged_at INTEGER,
       invalidated_at INTEGER,
       expires_at INTEGER
     );
     CREATE INDEX IF NOT EXISTS tag_times_by_age
       ON tag_times (MAX(COALESCE(purged_at, 0), COALESCE(invalidated_at, 0)));`,
  );
  const columns = sql.exec<{ name: string }>("PRAGMA table_info(tag_times)").toArray();
  if (!columns.some(({ name }) => name === "expires_at")) {
    sql.exec("ALTER TABLE tag_times ADD COLUMN expires_at INTEGER");
  }

  return {
    record(mode, tags) {
      const column = mode === "purge" ? "purged_at" : "invalidated_at";
      storage.transactionSync(() => {
        for (const { tag, raisedAt, expiresAt } of tags) {
          const expires = mode === "invalidate" ? (expiresAt ?? null) : null;
          sql.exec(
            `INSERT INTO tag_times (tag, ${column}, expires_at) VALUES (?, ?, ?)
             ON CONFLICT(tag) DO UPDATE SET
               ${column} = MAX(COALESCE(tag_times.${column}, 0), excluded.${column}),
               expires_at = CASE
                 WHEN excluded.expires_at IS NULL THEN tag_times.expires_at
                 ELSE MAX(COALESCE(tag_times.expires_at, 0), excluded.expires_at)
               END`,
            tag,
            raisedAt,
            expires,
          );
        }
      });
    },
    read(tags) {
      const times: TagTimes = {};
      for (let i = 0; i < tags.length; i += TAGS_PER_QUERY) {
        const chunk = tags.slice(i, i + TAGS_PER_QUERY);
        const rows = sql
          .exec<{
            tag: string;
            purged_at: number | null;
            invalidated_at: number | null;
            expires_at: number | null;
          }>(
            `SELECT tag, purged_at, invalidated_at, expires_at FROM tag_times
             WHERE tag IN (${chunk.map(() => "?").join(",")})`,
            ...chunk,
          )
          .toArray();
        for (const row of rows) {
          times[row.tag] = {
            ...(row.purged_at !== null && { purgedAt: row.purged_at }),
            ...(row.invalidated_at !== null && { invalidatedAt: row.invalidated_at }),
            ...(row.expires_at !== null && { expiresAt: row.expires_at }),
          };
        }
      }
      return times;
    },
    forgetBefore(at) {
      sql.exec(
        `DELETE FROM tag_times
         WHERE MAX(COALESCE(purged_at, 0), COALESCE(invalidated_at, 0)) < ?`,
        at,
      );
    },
  };
}
