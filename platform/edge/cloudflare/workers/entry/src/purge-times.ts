import { stampTag } from "@platform/edge-contract/cache-tags";
import type { PurgeMode } from "@platform/edge-contract/purge";
import type { RaisedTag } from "./purge-backlog";

export interface TagTime {
  purgedAt?: number;
  invalidatedAt?: number;
}

export type TagTimes = Record<string, TagTime>;

export interface ReleaseScope {
  slug: string;
  storageCoordinate: string;
}

export interface RecordedRelease extends ReleaseScope {
  release: string;
}

export interface TagTimesStore {
  record(release: RecordedRelease, mode: PurgeMode, tags: readonly RaisedTag[]): void;
  read(tags: readonly string[]): TagTimes;
  readReleasesCheckedBefore(at: number, limit: number): RecordedRelease[];
  markChecked(release: string, at: number): void;
  retire(release: string, checkedAt: number): void;
  forget(release: string): void;
}

const TAGS_PER_QUERY = 100;

function releaseTagRange(release: string): [string, string] {
  const from = stampTag(release, "");
  const last = from.length - 1;
  return [from, from.slice(0, last) + String.fromCharCode(from.charCodeAt(last) + 1)];
}

export function newSqliteTagTimes(storage: DurableObjectStorage): TagTimesStore {
  const { sql } = storage;
  sql.exec(
    `CREATE TABLE IF NOT EXISTS tag_times (
       tag TEXT PRIMARY KEY,
       purged_at INTEGER,
       invalidated_at INTEGER
     );
     CREATE TABLE IF NOT EXISTS recorded_releases (
       release TEXT PRIMARY KEY,
       slug TEXT NOT NULL,
       storage_coordinate TEXT NOT NULL,
       checked_at INTEGER NOT NULL
     );
     CREATE INDEX IF NOT EXISTS recorded_releases_by_check
       ON recorded_releases (checked_at);`,
  );

  const raiseTime = (column: string, tag: string, at: number) =>
    sql.exec(
      `INSERT INTO tag_times (tag, ${column}) VALUES (?, ?)
       ON CONFLICT(tag) DO UPDATE SET
         ${column} = MAX(COALESCE(tag_times.${column}, 0), excluded.${column})`,
      tag,
      at,
    );

  return {
    record(release, mode, tags) {
      const column = mode === "purge" ? "purged_at" : "invalidated_at";
      storage.transactionSync(() => {
        sql.exec(
          `INSERT INTO recorded_releases (release, slug, storage_coordinate, checked_at)
           VALUES (?, ?, ?, ?)
           ON CONFLICT(release) DO NOTHING`,
          release.release,
          release.slug,
          release.storageCoordinate,
          Date.now(),
        );
        for (const { tag, raisedAt } of tags) raiseTime(column, tag, raisedAt);
      });
    },
    read(tags) {
      const times: TagTimes = {};
      for (let i = 0; i < tags.length; i += TAGS_PER_QUERY) {
        const chunk = tags.slice(i, i + TAGS_PER_QUERY);
        const rows = sql
          .exec<{ tag: string; purged_at: number | null; invalidated_at: number | null }>(
            `SELECT tag, purged_at, invalidated_at FROM tag_times
             WHERE tag IN (${chunk.map(() => "?").join(",")})`,
            ...chunk,
          )
          .toArray();
        for (const row of rows) {
          times[row.tag] = {
            ...(row.purged_at !== null && { purgedAt: row.purged_at }),
            ...(row.invalidated_at !== null && { invalidatedAt: row.invalidated_at }),
          };
        }
      }
      return times;
    },
    readReleasesCheckedBefore(at, limit) {
      return sql
        .exec<{ release: string; slug: string; storage_coordinate: string }>(
          `SELECT release, slug, storage_coordinate FROM recorded_releases
           WHERE checked_at < ? ORDER BY checked_at LIMIT ?`,
          at,
          limit,
        )
        .toArray()
        .map((row) => ({
          release: row.release,
          slug: row.slug,
          storageCoordinate: row.storage_coordinate,
        }));
    },
    markChecked(release, at) {
      sql.exec(`UPDATE recorded_releases SET checked_at = ? WHERE release = ?`, at, release);
    },
    retire(release, checkedAt) {
      const [from, to] = releaseTagRange(release);
      storage.transactionSync(() => {
        const latest = sql
          .exec<{ at: number | null }>(
            `SELECT MAX(MAX(COALESCE(purged_at, 0), COALESCE(invalidated_at, 0))) AS at
             FROM tag_times WHERE tag >= ? AND tag < ?`,
            from,
            to,
          )
          .toArray()[0]?.at;
        sql.exec(`DELETE FROM tag_times WHERE tag >= ? AND tag < ?`, from, to);
        sql.exec(
          `UPDATE recorded_releases SET checked_at = ? WHERE release = ?`,
          checkedAt,
          release,
        );
        if (latest) raiseTime("purged_at", release, latest);
      });
    },
    forget(release) {
      const [from, to] = releaseTagRange(release);
      storage.transactionSync(() => {
        sql.exec(
          `DELETE FROM tag_times WHERE tag = ? OR (tag >= ? AND tag < ?)`,
          release,
          from,
          to,
        );
        sql.exec(`DELETE FROM recorded_releases WHERE release = ?`, release);
      });
    },
  };
}
